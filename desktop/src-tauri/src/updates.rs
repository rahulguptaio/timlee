//! Updates, for signed release builds — and never in the reader's way.
//!
//! The updater fetches only over https from the endpoint in tauri.conf.json
//! and verifies every download against the public key the build carries. A
//! debug build, a build without the key, or one pointed at anything but https
//! (a test server) registers no updater at all.
//!
//! Nothing is asked at launch. A background check downloads a new release
//! quietly and turns "Check for Updates…" into "Restart to Update Timlee X".
//! The app asks about it at most once, as a sheet on its own window, and only
//! when `should_ask` allows: the reader is signed in, away from the window,
//! has not said "Later" to that version, and has not been asked in a day.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use tauri::menu::MenuItem;
use tauri::{AppHandle, Manager, Wry};
use tauri_plugin_dialog::{
    DialogExt, MessageDialogBuilder, MessageDialogButtons, MessageDialogKind,
};
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::menu;

/// The first check waits for the app to settle; later ones run daily.
const FIRST_CHECK: Duration = Duration::from_secs(60);
const CHECK_EVERY: Duration = Duration::from_secs(24 * 60 * 60);
/// How often a downloaded update looks for a moment to ask.
const TICK: Duration = Duration::from_secs(60);
const DAY_SECONDS: u64 = 24 * 60 * 60;

/// What the window shows, as far as asking about updates is concerned.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Screen {
    Starting,
    SignIn,
    Offline,
    /// A page of the site: the reader is signed in.
    Site,
}

/// The last update the reader put off, kept in the app data directory.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Dismissed {
    pub version: String,
    /// Unix seconds.
    pub at: u64,
}

/// Whether to ask about a downloaded update right now.
///
/// Never on the sign-in, offline or starting screens, never while the reader
/// is using the window, never again for a version they put off, and at most
/// once a day for a newer one.
pub fn should_ask(
    current: &str,
    offered: &str,
    dismissed: Option<&Dismissed>,
    now: u64,
    screen: Screen,
    window_focused: bool,
) -> bool {
    let newer = matches!(
        (semver::Version::parse(offered), semver::Version::parse(current)),
        (Ok(offered), Ok(current)) if offered > current
    );
    if !newer || screen != Screen::Site || window_focused {
        return false;
    }
    match dismissed {
        Some(record) if record.version == offered => false,
        Some(record) => now >= record.at.saturating_add(DAY_SECONDS),
        None => true,
    }
}

/// Whether an updater configuration may be used: https endpoints only, and
/// none of the plugin's escape hatches. A build pointed at a test server
/// must not ship with a working updater.
pub fn endpoints_are_safe(updater: &serde_json::Value) -> bool {
    let endpoints = updater.get("endpoints").and_then(|e| e.as_array());
    let all_https = endpoints.is_some_and(|list| {
        !list.is_empty()
            && list
                .iter()
                .all(|url| url.as_str().is_some_and(|url| url.starts_with("https://")))
    });
    let escape = [
        "dangerousInsecureTransportProtocol",
        "dangerousAcceptInvalidCerts",
        "dangerousAcceptInvalidHostnames",
    ]
    .iter()
    .any(|key| updater.get(*key).and_then(|v| v.as_bool()) == Some(true));
    all_https && !escape
}

struct Ready {
    update: Update,
    bytes: Vec<u8>,
}

struct Updates {
    item: MenuItem<Wry>,
    ready: Mutex<Option<Ready>>,
    /// A question is on screen, or a check the reader asked for is running.
    busy: AtomicBool,
    record: PathBuf,
}

/// Registers the updater and returns its menu item, or does nothing for a
/// build that must not update itself.
pub fn register(app: &AppHandle) -> Option<MenuItem<Wry>> {
    let config = app.config().plugins.0.get("updater")?.clone();
    let has_key = config
        .get("pubkey")
        .and_then(|key| key.as_str())
        .is_some_and(|key| !key.trim().is_empty());
    if cfg!(debug_assertions) || !has_key {
        return None;
    }
    if !endpoints_are_safe(&config) {
        eprintln!("updater: off, because an endpoint is not plain https");
        return None;
    }
    if let Err(error) = app.plugin(tauri_plugin_updater::Builder::new().build()) {
        eprintln!("updater: off: {error}");
        return None;
    }
    let label = crate::locale(app).text("update.menu.check");
    let item = MenuItem::with_id(app, menu::CHECK_UPDATES, label, true, None::<&str>).ok()?;
    let record = app.path().app_data_dir().ok()?.join("updates.json");
    app.manage(Updates {
        item: item.clone(),
        ready: Mutex::new(None),
        busy: AtomicBool::new(false),
        record,
    });

    let app = app.clone();
    std::thread::spawn(move || {
        std::thread::sleep(FIRST_CHECK);
        let mut checked: Option<Instant> = None;
        loop {
            if checked.is_none_or(|at| at.elapsed() >= CHECK_EVERY) {
                checked = Some(Instant::now());
                if let Err(error) = fetch(&app) {
                    eprintln!("updater: check failed: {error}");
                }
            }
            ask_if_welcome(&app);
            std::thread::sleep(TICK);
        }
    });
    Some(item)
}

/// Puts the updater's item in the app's current language, for a menu built in
/// it, and returns it; `None` for a build without an updater.
pub fn relabel(app: &AppHandle) -> Option<MenuItem<Wry>> {
    let state = app.try_state::<Updates>()?;
    let locale = crate::locale(app);
    let text = match state.ready.lock().unwrap().as_ref() {
        Some(ready) => locale.format("update.menu.restart", &[("version", &ready.update.version)]),
        // Only a check the reader asked for marks the updater busy without
        // a download.
        None if state.busy.load(Ordering::SeqCst) => locale.text("update.menu.checking"),
        None => locale.text("update.menu.check"),
    };
    let _ = state.item.set_text(text);
    Some(state.item.clone())
}

/// "Check for Updates…" or, once one is downloaded, "Restart to Update".
pub fn menu_clicked(app: &AppHandle) {
    let state = app.state::<Updates>();
    if state.ready.lock().unwrap().is_some() {
        let app = app.clone();
        std::thread::spawn(move || install(&app));
        return;
    }
    if state.busy.swap(true, Ordering::SeqCst) {
        return;
    }
    let _ = state.item.set_enabled(false);
    let _ = state
        .item
        .set_text(crate::locale(app).text("update.menu.checking"));
    let app = app.clone();
    std::thread::spawn(move || {
        let state = app.state::<Updates>();
        let locale = crate::locale(&app);
        match fetch(&app) {
            Ok(true) => ask(&app),
            Ok(false) => {
                let _ = state.item.set_text(locale.text("update.menu.check"));
                let version = app.package_info().version.to_string();
                tell(
                    &app,
                    &locale.text("update.current.title"),
                    &locale.format("update.current.message", &[("version", &version)]),
                );
            }
            Err(error) => {
                eprintln!("updater: check failed: {error}");
                let _ = state.item.set_text(locale.text("update.menu.check"));
                tell(
                    &app,
                    &locale.text("update.checkFailed.title"),
                    &locale.text("update.checkFailed.message"),
                );
            }
        }
        let _ = state.item.set_enabled(true);
        state.busy.store(false, Ordering::SeqCst);
    });
}

/// Checks for a release and downloads it in the background. Returns whether
/// an update is ready to install.
fn fetch(app: &AppHandle) -> tauri_plugin_updater::Result<bool> {
    let state = app.state::<Updates>();
    tauri::async_runtime::block_on(async {
        let Some(update) = app.updater()?.check().await? else {
            // Nothing newer, or the release was withdrawn: forget any download.
            if state.ready.lock().unwrap().take().is_some() {
                let _ = state
                    .item
                    .set_text(crate::locale(app).text("update.menu.check"));
            }
            return Ok(false);
        };
        let known = state
            .ready
            .lock()
            .unwrap()
            .as_ref()
            .map(|r| r.update.version.clone());
        if known.as_deref() != Some(update.version.as_str()) {
            let bytes = update.download(|_, _| {}, || {}).await?;
            let _ = state.item.set_text(
                crate::locale(app).format("update.menu.restart", &[("version", &update.version)]),
            );
            *state.ready.lock().unwrap() = Some(Ready { update, bytes });
        }
        Ok(true)
    })
}

fn ask_if_welcome(app: &AppHandle) {
    let state = app.state::<Updates>();
    let Some(offered) = state
        .ready
        .lock()
        .unwrap()
        .as_ref()
        .map(|r| r.update.version.clone())
    else {
        return;
    };
    let (screen, focused) = crate::screen(app);
    let welcome = should_ask(
        &app.package_info().version.to_string(),
        &offered,
        read_dismissed(&state.record).as_ref(),
        unix_now(),
        screen,
        focused,
    );
    if welcome && !state.busy.swap(true, Ordering::SeqCst) {
        ask(app);
        state.busy.store(false, Ordering::SeqCst);
    }
}

/// Asks once, on the window. "Later" is remembered for this version.
fn ask(app: &AppHandle) {
    let state = app.state::<Updates>();
    let Some(version) = state
        .ready
        .lock()
        .unwrap()
        .as_ref()
        .map(|r| r.update.version.clone())
    else {
        return;
    };
    let locale = crate::locale(app);
    let restart = dialog(
        app,
        &locale.format("update.ready.title", &[("version", &version)]),
        locale.text("update.ready.message"),
    )
    .buttons(MessageDialogButtons::OkCancelCustom(
        locale.text("update.ready.restart"),
        locale.text("update.ready.later"),
    ))
    .blocking_show();
    if restart {
        install(app);
    } else {
        write_dismissed(
            &state.record,
            &Dismissed {
                version,
                at: unix_now(),
            },
        );
    }
}

fn install(app: &AppHandle) {
    let state = app.state::<Updates>();
    let Some(ready) = state.ready.lock().unwrap().take() else {
        return;
    };
    match ready.update.install(&ready.bytes) {
        Ok(()) => app.restart(),
        Err(error) => {
            eprintln!("updater: install failed: {error}");
            let locale = crate::locale(app);
            let _ = state.item.set_text(locale.text("update.menu.check"));
            tell(
                app,
                &locale.text("update.installFailed.title"),
                &locale.text("update.installFailed.message"),
            );
        }
    }
}

/// A sheet on Timlee's own window. Without a window, macOS shows a
/// free-standing system alert that takes keyboard focus from whatever the
/// reader is doing — one stray Return would answer it.
fn dialog(app: &AppHandle, title: &str, message: String) -> MessageDialogBuilder<Wry> {
    let builder = app
        .dialog()
        .message(message)
        .title(title)
        .kind(MessageDialogKind::Info);
    match app.get_webview_window(crate::MAIN) {
        Some(window) => builder.parent(&window),
        None => builder,
    }
}

fn tell(app: &AppHandle, title: &str, message: &str) {
    dialog(app, title, message.to_string()).blocking_show();
}

fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| since.as_secs())
}

fn read_dismissed(path: &Path) -> Option<Dismissed> {
    serde_json::from_slice(&std::fs::read(path).ok()?).ok()
}

fn write_dismissed(path: &Path, record: &Dismissed) {
    let written = path
        .parent()
        .map_or(Ok(()), std::fs::create_dir_all)
        .and_then(|()| std::fs::write(path, serde_json::to_vec(record).unwrap_or_default()));
    if let Err(error) = written {
        eprintln!("updater: could not remember the choice: {error}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const NOW: u64 = 1_800_000_000;
    const HOUR: u64 = 60 * 60;

    fn later(version: &str, hours_ago: u64) -> Dismissed {
        Dismissed {
            version: version.into(),
            at: NOW - hours_ago * HOUR,
        }
    }

    #[test]
    fn asks_only_when_signed_in_and_away() {
        let ask = |screen, focused| should_ask("0.1.0", "0.1.1", None, NOW, screen, focused);
        assert!(ask(Screen::Site, false));
        assert!(!ask(Screen::Site, true), "the reader is using the window");
        for screen in [Screen::Starting, Screen::SignIn, Screen::Offline] {
            assert!(!ask(screen, false), "{screen:?}");
        }
    }

    #[test]
    fn remembers_later() {
        let ask = |dismissed: &Dismissed, offered| {
            should_ask("0.1.0", offered, Some(dismissed), NOW, Screen::Site, false)
        };
        // The same version is never offered again, however long ago.
        assert!(!ask(&later("0.1.1", 1), "0.1.1"));
        assert!(!ask(&later("0.1.1", 24 * 365), "0.1.1"));
        // A newer one waits out a day since the reader was last asked.
        assert!(!ask(&later("0.1.1", 23), "0.1.2"));
        assert!(ask(&later("0.1.1", 24), "0.1.2"));
        // A clock set back does not wrap the arithmetic into "ask now".
        let future = Dismissed {
            version: "0.1.1".into(),
            at: u64::MAX,
        };
        assert!(!ask(&future, "0.1.2"));
    }

    #[test]
    fn never_offers_an_older_or_unreadable_version() {
        for offered in ["0.1.0", "0.0.9", "not-a-version", ""] {
            assert!(
                !should_ask("0.1.0", offered, None, NOW, Screen::Site, false),
                "{offered}"
            );
        }
        assert!(should_ask(
            "0.9.0",
            "0.10.0",
            None,
            NOW,
            Screen::Site,
            false
        ));
        assert!(should_ask(
            "1.0.0-beta.1",
            "1.0.0",
            None,
            NOW,
            Screen::Site,
            false
        ));
    }

    #[test]
    fn refuses_endpoints_that_are_not_plain_https() {
        let config = |json: &str| serde_json::from_str::<serde_json::Value>(json).unwrap();
        assert!(endpoints_are_safe(&config(
            r#"{"endpoints":["https://timlee.example/desktop/latest.json"]}"#
        )));
        for unsafe_config in [
            r#"{"endpoints":["http://127.0.0.1:19825/latest.json"]}"#,
            r#"{"endpoints":["https://a.example/l.json","http://b.example/l.json"]}"#,
            r#"{"endpoints":[]}"#,
            r#"{}"#,
            r#"{"endpoints":["https://a.example/l.json"],"dangerousInsecureTransportProtocol":true}"#,
            r#"{"endpoints":["https://a.example/l.json"],"dangerousAcceptInvalidCerts":true}"#,
            r#"{"endpoints":["https://a.example/l.json"],"dangerousAcceptInvalidHostnames":true}"#,
        ] {
            assert!(
                !endpoints_are_safe(&config(unsafe_config)),
                "{unsafe_config}"
            );
        }
    }

    #[test]
    fn keeps_the_choice_across_launches() {
        let dir = std::env::current_dir()
            .unwrap()
            .join("target")
            .join("updates-test");
        let path = dir.join("nested").join("updates.json");
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(read_dismissed(&path), None);
        write_dismissed(&path, &later("0.1.1", 0));
        assert_eq!(read_dismissed(&path), Some(later("0.1.1", 0)));
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
