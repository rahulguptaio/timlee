//! Timlee for macOS and Windows: a native window around the hosted Timlee site.
//!
//! The window loads the site rather than a bundled copy of it (docs/DESKTOP.md
//! explains why) and stays on it: `routes` decides where every navigation may
//! go, the pages bundled from ../src cover the moments the site cannot (no
//! network, signing in), and nothing loaded from the network can call into the
//! app — the only commands are granted to bundled pages (capabilities/). The
//! app's own words follow the site's language (`i18n`).

mod i18n;
mod menu;
mod routes;
mod signin;
mod updates;

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

use serde::Serialize;
use tauri::menu::MenuEvent;
use tauri::webview::{NewWindowResponse, PageLoadEvent, PageLoadPayload};
use tauri::window::Color;
use tauri::{
    AppHandle, Manager, State, Theme, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder, Window,
    WindowEvent,
};
use tauri_plugin_window_state::StateFlags;

use routes::Route;

/// The site this build opens: production for a release build, the local stack
/// (`go run ./cmd/timlee-dev`) for a development build. TIMLEE_DESKTOP_ORIGIN
/// at compile time overrides either, e.g. for staging or another local port.
const ORIGIN: &str = match option_env!("TIMLEE_DESKTOP_ORIGIN") {
    Some(origin) => origin,
    None if cfg!(debug_assertions) => "http://localhost:5173",
    None => "https://timlee.example",
};

pub(crate) const MAIN: &str = "main";

/// How long the window may stay hidden when no page reports that it loaded.
const REVEAL_DEADLINE: Duration = Duration::from_secs(8);

/// The zoom steps browsers offer. The webview's own zoom shortcuts are off, so
/// the View menu is the one way to scale the page, and it scales predictably.
const ZOOM_LEVELS: [f64; 13] = [
    0.5, 0.67, 0.75, 0.8, 0.9, 1.0, 1.1, 1.25, 1.5, 1.75, 2.0, 2.5, 3.0,
];
const ZOOM_DEFAULT: usize = 5;

/// The webview's own context menu offers Reload, Back and Forward: browser
/// controls that do not belong in an app. Text fields and selections keep it,
/// for Cut, Copy, Paste and spelling. (Release builds have no inspector.)
#[cfg(not(debug_assertions))]
const QUIET_CONTEXT_MENU: &str = r#"
document.addEventListener('contextmenu', (event) => {
  const target = event.target instanceof Element ? event.target : null;
  if (target?.closest('input, textarea, [contenteditable]:not([contenteditable="false"])')) return;
  if (String(getSelection() ?? '')) return;
  event.preventDefault();
}, true);
"#;

/// Submits the site's own sign-out form, so the server ends the session and
/// clears the cookie exactly as it does for a click in the page.
const SIGN_OUT: &str = r#"(() => {
  const form = document.querySelector('form[action="/sign-out"]')
    ?? Object.assign(document.body.appendChild(document.createElement('form')),
                     { method: 'post', action: '/sign-out' });
  form.requestSubmit();
})()"#;

/// What a page rendered, for the smoke test (TIMLEE_DESKTOP_REPORT=1). Reads
/// the DOM the webview actually painted; the query string is left out because
/// it can carry one-time codes.
const REPORT: &str = r#"(() => {
  const root = document.documentElement;
  return {
    url: location.origin + location.pathname,
    title: document.title,
    dataTheme: root.dataset.theme ?? null,
    colorScheme: getComputedStyle(root).colorScheme,
    prefersDark: matchMedia('(prefers-color-scheme: dark)').matches,
    background: getComputedStyle(document.body).backgroundColor,
    signedIn: Boolean(document.querySelector('form[action="/sign-out"]')),
    heading: document.querySelector('h1:not([hidden] h1)')?.textContent.trim().replace(/\s+/g, ' ') ?? null,
  };
})()"#;

struct Shell {
    origin: Url,
    revealed: AtomicBool,
    /// What the window shows, so updates never ask on the sign-in or
    /// offline screens.
    screen: Mutex<updates::Screen>,
    /// Where the reader is headed: their apps, or the page that a sign-in
    /// interrupted.
    return_to: Mutex<String>,
    signin: signin::SignIn,
    zoom: Mutex<usize>,
    report: bool,
    /// URLs the first page asks to navigate to, so a smoke test can watch the
    /// navigation policy decide (TIMLEE_DESKTOP_SMOKE_NAVIGATE, space separated).
    smoke_navigations: Mutex<Vec<String>>,
    language: Mutex<Language>,
}

/// The language of the app's own words, and the site's as its cookie last
/// said, if it has.
#[derive(Clone)]
struct Language {
    words: &'static i18n::Locale,
    site: Option<String>,
}

impl Language {
    /// The language for the browser's sign-in page: the site's, which it
    /// serves by definition, else the app's.
    fn sign_in(&self) -> &str {
        self.site.as_deref().unwrap_or(self.words.id)
    }
}

impl Shell {
    fn page(&self, path: &str) -> Url {
        self.origin
            .join(path)
            .unwrap_or_else(|_| self.origin.clone())
    }

    fn return_to(&self) -> String {
        self.return_to.lock().unwrap().clone()
    }

    fn host(&self) -> String {
        let host = self.origin.host_str().unwrap_or_default();
        match self.origin.port() {
            Some(port) => format!("{host}:{port}"),
            None => host.to_string(),
        }
    }

    fn language(&self) -> Language {
        self.language.lock().unwrap().clone()
    }
}

/// The language of the app's own words right now.
pub(crate) fn locale(app: &AppHandle) -> &'static i18n::Locale {
    app.state::<Shell>().language().words
}

/// Follows the site's language: reads it from the window's cookie and, when
/// it changes the app's own, rebuilds the menu in it. Waits on the webview,
/// so it runs off the main thread (on Windows a synchronous command or event
/// handler would deadlock, tauri-apps/wry#583).
fn follow_site_language(window: &WebviewWindow) -> Language {
    let shell = window.state::<Shell>();
    let site = window
        .cookies_for_url(shell.origin.clone())
        .unwrap_or_default()
        .into_iter()
        .find(|cookie| cookie.name() == i18n::COOKIE)
        .map(|cookie| cookie.value().to_string());
    let words = i18n::choose(&i18n::LOCALES, site.as_deref(), &i18n::system_languages());
    let language = Language { words, site };
    let previous = std::mem::replace(&mut *shell.language.lock().unwrap(), language.clone());
    if !std::ptr::eq(previous.words, words) {
        rebuild_menu(window.app_handle());
    }
    language
}

fn rebuild_menu(app: &AppHandle) {
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        let updates = updates::relabel(&handle);
        let rebuilt = menu::build(&handle, locale(&handle), updates.as_ref())
            .and_then(|menu| handle.set_menu(menu).map(drop));
        if let Err(error) = rebuilt {
            eprintln!("menu: could not rebuild it in the new language: {error}");
        }
    });
}

/// A page compiled into the app. Tauri serves them from `tauri://localhost`,
/// and on Windows from `http://tauri.localhost`.
fn bundled(page: &str) -> Url {
    let base = if cfg!(windows) {
        "http://tauri.localhost/"
    } else {
        "tauri://localhost/"
    };
    Url::parse(base)
        .and_then(|base| base.join(page))
        .expect("a bundled page path")
}

fn signin_page(error: Option<&str>) -> Url {
    let mut page = bundled("signin.html");
    if let Some(code) = error {
        page.query_pairs_mut().append_pair("error", code);
    }
    page
}

/// Logged without query or fragment, which can carry one-time codes.
fn log(decision: &str, url: &Url) {
    let mut shown = url.clone();
    shown.set_query(None);
    shown.set_fragment(None);
    println!("navigation: {decision} {shown}");
}

/// What the window shows and whether the reader is using it.
pub(crate) fn screen(app: &AppHandle) -> (updates::Screen, bool) {
    let screen = *app.state::<Shell>().screen.lock().unwrap();
    let focused = app
        .get_webview_window(MAIN)
        .and_then(|window| window.is_focused().ok())
        .unwrap_or(false);
    (screen, focused)
}

/// Shows the window once there is something in it, so nobody ever sees an
/// empty white rectangle while the page loads. It does not take focus: the
/// system already activates an app the reader launched, and one started in
/// the background (`open -g`) must stay there.
fn reveal_window(window: &WebviewWindow) {
    if !window
        .state::<Shell>()
        .revealed
        .swap(true, Ordering::SeqCst)
    {
        let _ = window.show();
    }
}

/// Brings an already-revealed window back: a second launch, a click on the
/// Dock icon, a finished sign-in. Before the first reveal it does nothing —
/// the window appears on its own once its page has loaded.
fn bring_to_front(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN) else {
        return;
    };
    if window.state::<Shell>().revealed.load(Ordering::SeqCst) {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

/// Points the window at `url` from another thread, so it never runs inside a
/// navigation callback that WebKit is still waiting on.
fn open_in_window(app: &AppHandle, url: Url) {
    let app = app.clone();
    std::thread::spawn(move || {
        if let Some(window) = app.get_webview_window(MAIN) {
            let _ = window.navigate(url);
        }
    });
}

/// Hands a URL to the system browser, if the one gate in `routes` allows it.
fn open_in_browser(app: &AppHandle, url: &Url) -> Result<(), ()> {
    if !routes::may_open_in_browser(url, &app.state::<Shell>().origin) {
        log("refused", url);
        return Err(());
    }
    log("browser", url);
    // Development builds can stand a script in for the browser, so the whole
    // sign-in runs unattended (desktop/scripts/smoke-macos.sh). Release
    // builds never run a program named by the environment.
    #[cfg(debug_assertions)]
    if let Some(program) = std::env::var_os("TIMLEE_DESKTOP_BROWSER") {
        return std::process::Command::new(program)
            .arg(url.as_str())
            .spawn()
            .map(drop)
            .map_err(|error| eprintln!("navigation: TIMLEE_DESKTOP_BROWSER failed: {error}"));
    }
    tauri_plugin_opener::open_url(url.as_str(), None::<&str>).map_err(|error| {
        eprintln!("navigation: the browser did not open: {error}");
    })
}

/// The navigation policy, applied to every navigation and new-window request.
/// Returns whether the window itself may load `url`.
fn decide(app: &AppHandle, url: &Url) -> bool {
    let shell = app.state::<Shell>();
    match routes::route(url, &shell.origin) {
        Route::InApp => true,
        Route::Home => {
            open_in_window(app, shell.page(routes::APPS_PATH));
            false
        }
        Route::SignIn { return_to, error } => {
            log("sign-in", url);
            *shell.return_to.lock().unwrap() = return_to;
            open_in_window(app, signin_page(error.as_deref()));
            false
        }
        Route::BrowserSignIn => {
            // The window keeps the bundled page, which waits for the browser.
            let target = routes::with_locale(url, shell.language().sign_in());
            let app = app.clone();
            std::thread::spawn(move || {
                if open_in_browser(&app, &target).is_err() {
                    app.state::<Shell>().signin.cancel();
                    open_in_window(&app, signin_page(Some("browser")));
                }
            });
            false
        }
        Route::External => {
            let (app, url) = (app.clone(), url.clone());
            std::thread::spawn(move || open_in_browser(&app, &url));
            false
        }
        Route::Refused => {
            log("refused", url);
            false
        }
    }
}

fn page_loaded(window: &WebviewWindow, payload: &PageLoadPayload<'_>) {
    if !matches!(payload.event(), PageLoadEvent::Finished) {
        return;
    }
    let url = payload.url();
    let shell = window.state::<Shell>();
    // The boot page decides for itself whether there is anything to show:
    // normally it hands over to the site before anyone sees it.
    let booting = routes::is_bundled(url) && matches!(url.path(), "/" | "/index.html");
    if !booting {
        reveal_window(window);
    }
    *shell.screen.lock().unwrap() = match (routes::is_bundled(url), booting) {
        (false, _) => updates::Screen::Site,
        (true, true) => updates::Screen::Starting,
        (true, false) => updates::Screen::SignIn,
    };
    if !routes::is_bundled(url) {
        // A page of the site may have just changed its language.
        let window = window.clone();
        std::thread::spawn(move || follow_site_language(&window));
    }
    if shell.report {
        let _ = window.eval_with_callback(REPORT, |page| println!("page: {page}"));
    }
    if !booting {
        let urls = std::mem::take(&mut *shell.smoke_navigations.lock().unwrap());
        if !urls.is_empty() {
            let window = window.clone();
            std::thread::spawn(move || {
                for url in urls {
                    std::thread::sleep(Duration::from_millis(700));
                    let target = serde_json::to_string(&url).unwrap_or_default();
                    let _ = window.eval(format!("location.assign({target})"));
                }
            });
        }
    }
}

fn background(theme: Theme) -> Color {
    // The site's page colours (--ts-background), so a resize never flashes
    // a different colour at the window's edge.
    match theme {
        Theme::Dark => Color(0x0b, 0x14, 0x24, 0xff),
        _ => Color(0xff, 0xff, 0xff, 0xff),
    }
}

fn create_window(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    let (navigations, popups) = (app.clone(), app.clone());
    let builder = WebviewWindowBuilder::new(app, MAIN, WebviewUrl::App("index.html".into()))
        .title("Timlee")
        .inner_size(1280.0, 840.0)
        .min_inner_size(900.0, 600.0)
        .center()
        // Shown by `reveal_window` once a page has loaded.
        .visible(false)
        .on_navigation(move |url| decide(&navigations, url))
        .on_new_window(move |url, _features| {
            // One window: a link that asks for another opens here, or in the
            // system browser if it leaves the site.
            if decide(&popups, &url) {
                open_in_window(&popups, url);
            }
            NewWindowResponse::Deny
        })
        .on_page_load(|window, payload| page_loaded(&window, &payload))
        .on_document_title_changed(|window, title| {
            let _ = window.set_title(&title);
        });
    #[cfg(not(debug_assertions))]
    let builder = builder.initialization_script(QUIET_CONTEXT_MENU);

    let window = builder.build()?;
    let _ = window.set_background_color(Some(background(window.theme().unwrap_or(Theme::Light))));
    Ok(window)
}

fn on_window_event(window: &Window, event: &WindowEvent) {
    match event {
        // macOS keeps an app running when its last window closes; the Dock
        // icon brings it back (RunEvent::Reopen), loaded and where it was.
        #[cfg(target_os = "macos")]
        WindowEvent::CloseRequested { api, .. } if window.label() == MAIN => {
            api.prevent_close();
            let _ = window.hide();
        }
        WindowEvent::ThemeChanged(theme) => {
            let _ = window.set_background_color(Some(background(*theme)));
        }
        _ => {}
    }
}

fn on_menu(app: &AppHandle, event: MenuEvent) {
    let Some(window) = app.get_webview_window(MAIN) else {
        return;
    };
    let shell = app.state::<Shell>();
    match event.id().as_ref() {
        menu::SETTINGS => {
            bring_to_front(app);
            let _ = window.navigate(shell.page("/settings"));
        }
        menu::DOCS => {
            bring_to_front(app);
            let _ = window.navigate(shell.page("/docs"));
        }
        menu::SIGN_OUT => {
            if window
                .url()
                .is_ok_and(|url| url.origin() == shell.origin.origin())
            {
                let _ = window.eval(SIGN_OUT);
            }
        }
        menu::RELOAD => {
            let _ = window.reload();
        }
        id @ (menu::ZOOM_IN | menu::ZOOM_OUT | menu::ZOOM_RESET) => {
            let mut level = shell.zoom.lock().unwrap();
            *level = match id {
                menu::ZOOM_IN => (*level + 1).min(ZOOM_LEVELS.len() - 1),
                menu::ZOOM_OUT => level.saturating_sub(1),
                _ => ZOOM_DEFAULT,
            };
            let _ = window.set_zoom(ZOOM_LEVELS[*level]);
        }
        menu::FULL_SCREEN => {
            let full = window.is_fullscreen().unwrap_or(false);
            let _ = window.set_fullscreen(!full);
        }
        menu::CHECK_UPDATES => {
            bring_to_front(app);
            updates::menu_clicked(app);
        }
        _ => {}
    }
}

/* ------------------------------------------------- commands for bundled pages */

#[derive(Serialize)]
struct Boot {
    /// Where to go once the site answers.
    target: String,
    /// A cheap, static URL whose answer proves the site is reachable.
    probe: String,
    /// The site's host, for the words on the page.
    host: String,
    /// The page's language: `lang`, `dir`, the `Intl` locale for times, and
    /// its messages (`i18n`).
    lang: &'static str,
    dir: &'static str,
    intl: &'static str,
    messages: BTreeMap<String, String>,
}

/// What a bundled page needs to know, in the language the site's cookie
/// names now. Async, so reading the cookie never blocks the main thread.
#[tauri::command]
async fn boot(window: WebviewWindow) -> Boot {
    let words = follow_site_language(&window).words;
    let shell = window.state::<Shell>();
    Boot {
        target: shell.page(&shell.return_to()).to_string(),
        probe: shell.page("/favicon.svg").to_string(),
        host: shell.host(),
        lang: words.lang,
        dir: words.dir,
        intl: words.intl,
        messages: words.page_messages(),
    }
}

/// Called by the boot page when the site is unreachable, which is the only
/// time it shows itself.
#[tauri::command]
fn reveal(window: WebviewWindow) {
    *window.state::<Shell>().screen.lock().unwrap() = updates::Screen::Offline;
    reveal_window(&window);
    if window.state::<Shell>().report {
        let _ = window.eval_with_callback(REPORT, |page| println!("page: {page}"));
    }
}

/// Starts a sign-in (see `signin`). The window opens the site's start page,
/// which sets the flow cookie in it and redirects to the sign-in page that
/// `decide` hands to the system browser. The error is a code the sign-in page
/// turns into a sentence.
#[tauri::command]
async fn sign_in(app: AppHandle, shell: State<'_, Shell>) -> Result<(), &'static str> {
    let handle = app.clone();
    let start = shell
        .signin
        .start(
            &shell.origin,
            &shell.return_to(),
            signin::return_page(locale(&app)),
            move |done| {
                let next = match done {
                    signin::Done::Completed(url) => url,
                    signin::Done::TimedOut => signin_page(Some("timeout")),
                    signin::Done::Failed(error) => {
                        eprintln!("sign-in: the listener failed: {error}");
                        signin_page(Some("listener"))
                    }
                };
                open_in_window(&handle, next);
                bring_to_front(&handle);
            },
        )
        .map_err(|error| {
            eprintln!("sign-in: could not listen on 127.0.0.1: {error}");
            "listener"
        })?;
    open_in_window(&app, start);
    Ok(())
}

#[tauri::command]
fn cancel_sign_in(shell: State<'_, Shell>) {
    shell.signin.cancel();
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let origin = Url::parse(ORIGIN).expect("TIMLEE_DESKTOP_ORIGIN must be an absolute URL");
    let shell = Shell {
        origin,
        revealed: AtomicBool::new(false),
        screen: Mutex::new(updates::Screen::Starting),
        return_to: Mutex::new(routes::APPS_PATH.into()),
        signin: signin::SignIn::default(),
        zoom: Mutex::new(ZOOM_DEFAULT),
        report: std::env::var_os("TIMLEE_DESKTOP_REPORT").is_some_and(|value| value == "1"),
        smoke_navigations: Mutex::new(
            std::env::var("TIMLEE_DESKTOP_SMOKE_NAVIGATE")
                .map(|urls| urls.split_whitespace().map(String::from).collect())
                .unwrap_or_default(),
        ),
        // The system's language until the site's cookie says otherwise.
        language: Mutex::new(Language {
            words: i18n::choose(&i18n::LOCALES, None, &i18n::system_languages()),
            site: None,
        }),
    };

    let mut builder = tauri::Builder::default();
    // Registered first, so a second launch hands over and exits before any
    // other plugin starts (tauri.app/plugin/single-instance).
    #[cfg(desktop)]
    {
        builder = builder.plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            println!("single-instance: another launch handed over to this one");
            bring_to_front(app);
        }));
    }
    let app = builder
        // Size and place, never visibility: the window stays hidden until its
        // page has loaded, whatever it was when the app last quit.
        .plugin(
            tauri_plugin_window_state::Builder::default()
                .with_state_flags(
                    StateFlags::SIZE
                        | StateFlags::POSITION
                        | StateFlags::MAXIMIZED
                        | StateFlags::FULLSCREEN,
                )
                .build(),
        )
        .plugin(tauri_plugin_dialog::init())
        .manage(shell)
        .invoke_handler(tauri::generate_handler![
            boot,
            reveal,
            sign_in,
            cancel_sign_in
        ])
        .on_menu_event(on_menu)
        .on_window_event(on_window_event)
        .setup(|app| {
            let handle = app.handle();
            let shell = handle.state::<Shell>();
            println!(
                "timlee: {} ({}) opening {}",
                handle.package_info().version,
                env!("TIMLEE_DESKTOP_COMMIT"),
                shell.origin
            );
            let updates = updates::register(handle);
            app.set_menu(menu::build(handle, locale(handle), updates.as_ref())?)?;
            let window = create_window(handle)?;
            // The site's language, if the window kept its cookie from an
            // earlier session.
            let site = window.clone();
            std::thread::spawn(move || follow_site_language(&site));
            // If no page ever reports in — a broken bundle, a hung load — show
            // the window anyway, so a failure is visible instead of an app
            // that seems never to have started.
            std::thread::spawn(move || {
                std::thread::sleep(REVEAL_DEADLINE);
                reveal_window(&window);
            });
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("Timlee failed to start");

    app.run(|_app, _event| {
        #[cfg(target_os = "macos")]
        if let tauri::RunEvent::Reopen {
            has_visible_windows: false,
            ..
        } = _event
        {
            bring_to_front(_app);
        }
    });
}
