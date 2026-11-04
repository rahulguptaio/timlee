//! The native menu bar, laid out the way each platform expects.
//!
//! macOS needs the Edit menu even though the app adds nothing to it: its
//! standard items are what make ⌘C, ⌘V and ⌘A work inside the webview.
//! WebView2 handles those keys itself, so Windows gets no Edit menu.
//!
//! Every label comes from the app's catalog (`i18n`), the standard items'
//! included, so the menus are in the app's language rather than whatever
//! AppKit or Windows would write for an item without a label.

use tauri::menu::{AboutMetadata, IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::{AppHandle, Wry};

use crate::i18n::Locale;

pub const SETTINGS: &str = "settings";
pub const SIGN_OUT: &str = "sign-out";
pub const RELOAD: &str = "reload";
pub const ZOOM_IN: &str = "zoom-in";
pub const ZOOM_OUT: &str = "zoom-out";
pub const ZOOM_RESET: &str = "zoom-reset";
pub const DOCS: &str = "docs";
pub const CHECK_UPDATES: &str = "check-updates";
pub const FULL_SCREEN: &str = "full-screen";

fn item(
    app: &AppHandle,
    id: &str,
    text: &str,
    accelerator: Option<&str>,
) -> tauri::Result<MenuItem<Wry>> {
    MenuItem::with_id(app, id, text, true, accelerator)
}

/// "Version 0.1.0 (3a2a83792b65)": the release, then the commit it was built
/// from. macOS words the panel itself; on Windows the dialog's labels are
/// muda's, in English.
fn about(app: &AppHandle, locale: &Locale) -> tauri::Result<PredefinedMenuItem<Wry>> {
    let metadata = AboutMetadata {
        name: Some("Timlee".into()),
        version: Some(app.package_info().version.to_string()),
        short_version: Some(env!("TIMLEE_DESKTOP_COMMIT").into()),
        copyright: app.config().bundle.copyright.clone(),
        icon: app.default_window_icon().cloned(),
        ..Default::default()
    };
    PredefinedMenuItem::about(app, Some(&locale.text("menu.app.about")), Some(metadata))
}

/// The menu bar in `locale`. `updates` is the updater's item ("Check for
/// Updates…", which becomes "Restart to Update"); only a signed release build
/// has one.
pub fn build(
    app: &AppHandle,
    locale: &Locale,
    updates: Option<&MenuItem<Wry>>,
) -> tauri::Result<Menu<Wry>> {
    let t = |key: &str| locale.text(key);
    let separator = || PredefinedMenuItem::separator(app);
    let settings = item(app, SETTINGS, &t("menu.app.settings"), Some("CmdOrCtrl+,"))?;
    let sign_out = item(app, SIGN_OUT, &t("menu.app.signOut"), None)?;
    let docs = item(app, DOCS, &t("menu.help.docs"), None)?;

    // macOS renames its full-screen item itself as the window enters and
    // leaves full screen.
    #[cfg(target_os = "macos")]
    let full_screen = PredefinedMenuItem::fullscreen(app, Some(&t("menu.view.fullScreen")))?;
    #[cfg(not(target_os = "macos"))]
    let full_screen = item(app, FULL_SCREEN, &t("menu.view.fullScreen"), Some("F11"))?;
    let view = Submenu::with_items(
        app,
        t("menu.view.title"),
        true,
        &[
            &item(app, RELOAD, &t("menu.view.reload"), Some("CmdOrCtrl+R"))?,
            &separator()?,
            &item(
                app,
                ZOOM_RESET,
                &t("menu.view.actualSize"),
                Some("CmdOrCtrl+0"),
            )?,
            &item(app, ZOOM_IN, &t("menu.view.zoomIn"), Some("CmdOrCtrl+="))?,
            &item(app, ZOOM_OUT, &t("menu.view.zoomOut"), Some("CmdOrCtrl+-"))?,
            &separator()?,
            &full_screen,
        ],
    )?;

    #[cfg(target_os = "macos")]
    {
        let about = about(app, locale)?;
        let (s1, s2, s3, s4) = (separator()?, separator()?, separator()?, separator()?);
        let services = PredefinedMenuItem::services(app, Some(&t("menu.app.services")))?;
        let hide = PredefinedMenuItem::hide(app, Some(&t("menu.app.hide")))?;
        let hide_others = PredefinedMenuItem::hide_others(app, Some(&t("menu.app.hideOthers")))?;
        let show_all = PredefinedMenuItem::show_all(app, Some(&t("menu.app.showAll")))?;
        let quit = PredefinedMenuItem::quit(app, Some(&t("menu.app.quit")))?;
        let mut first: Vec<&dyn IsMenuItem<Wry>> = vec![&about];
        if let Some(updates) = updates {
            first.push(updates);
        }
        first.extend([
            &s1 as &dyn IsMenuItem<Wry>,
            &settings,
            &sign_out,
            &s2,
            &services,
            &s3,
            &hide,
            &hide_others,
            &show_all,
            &s4,
            &quit,
        ]);
        // The product's name, which macOS shows in bold whatever this says.
        let timlee = Submenu::with_items(app, "Timlee", true, &first)?;
        let file = Submenu::with_items(
            app,
            t("menu.file.title"),
            true,
            &[&PredefinedMenuItem::close_window(
                app,
                Some(&t("menu.file.closeWindow")),
            )?],
        )?;
        let edit = Submenu::with_items(
            app,
            t("menu.edit.title"),
            true,
            &[
                &PredefinedMenuItem::undo(app, Some(&t("menu.edit.undo")))?,
                &PredefinedMenuItem::redo(app, Some(&t("menu.edit.redo")))?,
                &separator()?,
                &PredefinedMenuItem::cut(app, Some(&t("menu.edit.cut")))?,
                &PredefinedMenuItem::copy(app, Some(&t("menu.edit.copy")))?,
                &PredefinedMenuItem::paste(app, Some(&t("menu.edit.paste")))?,
                &PredefinedMenuItem::select_all(app, Some(&t("menu.edit.selectAll")))?,
            ],
        )?;
        let window = Submenu::with_items(
            app,
            t("menu.window.title"),
            true,
            &[
                &PredefinedMenuItem::minimize(app, Some(&t("menu.window.minimize")))?,
                &PredefinedMenuItem::maximize(app, Some(&t("menu.window.zoom")))?,
                &separator()?,
                &PredefinedMenuItem::bring_all_to_front(
                    app,
                    Some(&t("menu.window.bringAllToFront")),
                )?,
            ],
        )?;
        window.set_as_windows_menu_for_nsapp()?;
        let help = Submenu::with_items(app, t("menu.help.title"), true, &[&docs])?;
        help.set_as_help_menu_for_nsapp()?;
        Menu::with_items(app, &[&timlee, &file, &edit, &view, &window, &help])
    }

    #[cfg(not(target_os = "macos"))]
    {
        let file = Submenu::with_items(
            app,
            t("menu.file.title"),
            true,
            &[
                &settings,
                &sign_out,
                &separator()?,
                &PredefinedMenuItem::quit(app, Some(&t("menu.file.exit")))?,
            ],
        )?;
        let about = about(app, locale)?;
        let sep = separator()?;
        let mut last: Vec<&dyn IsMenuItem<Wry>> = vec![&docs];
        if let Some(updates) = updates {
            last.push(updates);
        }
        last.extend([&sep as &dyn IsMenuItem<Wry>, &about]);
        let help = Submenu::with_items(app, t("menu.help.title"), true, &last)?;
        Menu::with_items(app, &[&file, &view, &help])
    }
}
