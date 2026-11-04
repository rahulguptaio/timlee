//! Where each navigation the webview attempts is allowed to go.
//!
//! The window shows one site — the Timlee origin it was built for — plus the
//! pages bundled into the app. Everything else is either handed to the
//! system browser (https only) or refused, so a link can never turn the app
//! window into a general-purpose browser wearing Timlee's chrome. The site's
//! pages in another language (`/de/…`) follow the same rules as its English
//! ones.

use tauri::Url;

/// What to do with a URL the webview is about to load.
#[derive(Debug, PartialEq, Eq)]
pub enum Route {
    /// Load it in the app window.
    InApp,
    /// The site's landing page. An installed app has no use for marketing,
    /// so it opens the apps page instead.
    Home,
    /// The site's sign-in page. RFC 8252 §8.12: a native app signs in through
    /// the system browser, never inside its own webview.
    SignIn {
        return_to: String,
        error: Option<String>,
    },
    /// The site's sign-in page for a sign-in this app started (`desktop=1`),
    /// where `/sign-in/desktop/start` sends the window once it has set the
    /// flow cookie: the page opens in the system browser, and the window stays
    /// where it is.
    BrowserSignIn,
    /// Another https site: open it in the system browser.
    External,
    /// Anything else — plain http elsewhere, file:, custom schemes.
    Refused,
}

/// The page a signed-in reader lands on.
pub const APPS_PATH: &str = "/apps";

/// The site's default language, whose pages have no locale segment
/// (docs/I18N.md §2).
pub const DEFAULT_LOCALE: &str = "en";

pub fn route(url: &Url, origin: &Url) -> Route {
    if is_bundled(url) || url.scheme() == "about" {
        return Route::InApp;
    }
    if url.origin() != origin.origin() {
        return if url.scheme() == "https" {
            Route::External
        } else {
            Route::Refused
        };
    }
    let query = |name: &str| {
        url.query_pairs()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.into_owned())
    };
    match strip_locale(url.path()) {
        "/" => Route::Home,
        "/sign-in" if query("desktop").as_deref() == Some("1") => Route::BrowserSignIn,
        "/sign-in" => Route::SignIn {
            return_to: query("returnTo")
                .filter(|path| is_local_path(path))
                .unwrap_or_else(|| APPS_PATH.into()),
            error: query("error").filter(|code| !code.is_empty()),
        },
        _ => Route::InApp,
    }
}

/// A site path without its locale segment: "/de/sign-in" is "/sign-in", and
/// "/ar" and "/ar/" are "/". A segment is told by its shape, not looked up in
/// a list, so a language the site adds before the app knows of it still
/// routes: its sign-in page is never shown in the window.
pub fn strip_locale(path: &str) -> &str {
    match locale_segment(path) {
        Some(segment) => match &path[1 + segment.len()..] {
            "" => "/",
            rest => rest,
        },
        None => path,
    }
}

/// `url` in the site's `locale`: "/sign-in?…" becomes "/de/sign-in?…". The
/// default language has no segment, and a path that names one keeps it.
pub fn with_locale(url: &Url, locale: &str) -> Url {
    let mut localized = url.clone();
    if locale != DEFAULT_LOCALE && is_locale_id(locale) && locale_segment(url.path()).is_none() {
        localized.set_path(&format!("/{locale}{}", url.path()));
    }
    localized
}

fn locale_segment(path: &str) -> Option<&str> {
    let segment = path.strip_prefix('/')?.split('/').next()?;
    is_locale_id(segment).then_some(segment)
}

/// A locale ID as the site writes one in a URL: a lowercase language code of
/// two or three letters, then subtags of two to eight lowercase letters or
/// digits ("de", "pt-br", "ar-xb").
pub fn is_locale_id(value: &str) -> bool {
    let mut parts = value.split('-');
    let language = parts.next().unwrap_or_default();
    (2..=3).contains(&language.len())
        && language.bytes().all(|b| b.is_ascii_lowercase())
        && parts.all(|part| {
            (2..=8).contains(&part.len())
                && part
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
        })
}

/// Whether the URL is one of the pages compiled into the app: `tauri://localhost`
/// on macOS and Linux, `http://tauri.localhost` on Windows.
pub fn is_bundled(url: &Url) -> bool {
    url.scheme() == "tauri" || url.host_str() == Some("tauri.localhost")
}

/// The system browser only ever receives https URLs — or the app's own
/// origin, which is https in every shipped build and plain http only in a
/// development build pointed at a local stack. This is the one gate every
/// external open goes through.
pub fn may_open_in_browser(url: &Url, origin: &Url) -> bool {
    url.scheme() == "https" || url.origin() == origin.origin()
}

/// A same-site path, never a scheme-relative or absolute URL that would
/// redirect the reader somewhere else after signing in.
fn is_local_path(path: &str) -> bool {
    path.starts_with('/') && !path.starts_with("//") && !path.contains('\\')
}

#[cfg(test)]
mod tests {
    use super::*;

    fn url(value: &str) -> Url {
        Url::parse(value).unwrap()
    }

    fn sign_in(return_to: &str, error: Option<&str>) -> Route {
        Route::SignIn {
            return_to: return_to.into(),
            error: error.map(Into::into),
        }
    }

    #[test]
    fn routes_navigations() {
        let prod = url("https://timlee.example");
        let dev = url("http://localhost:19822");
        let cases = [
            (&prod, "https://timlee.example/apps", Route::InApp),
            (
                &prod,
                "https://timlee.example/settings/keys",
                Route::InApp,
            ),
            (
                &prod,
                "https://timlee.example/docs/api#auth",
                Route::InApp,
            ),
            (
                &prod,
                "https://timlee.example/sign-in/desktop/complete?code=c",
                Route::InApp,
            ),
            // A sign-in starts in the window, where the site sets the flow
            // cookie, and goes on to the browser.
            (
                &prod,
                "https://timlee.example/sign-in/desktop/start?port=51234&code_challenge=abc&code_challenge_method=S256&returnTo=%2Fapps",
                Route::InApp,
            ),
            (
                &prod,
                "https://timlee.example/sign-in?desktop=1&port=51234&code_challenge=abc&code_challenge_method=S256&flow=f&returnTo=%2Fapps",
                Route::BrowserSignIn,
            ),
            (
                &prod,
                "https://timlee.example/sign-in?desktop=0&returnTo=%2Fsettings",
                sign_in("/settings", None),
            ),
            (
                &prod,
                "https://timlee.example/sign-in?error=desktop_flow",
                sign_in("/apps", Some("desktop_flow")),
            ),
            (&prod, "https://timlee.example/", Route::Home),
            (
                &prod,
                "https://timlee.example/sign-in",
                sign_in("/apps", None),
            ),
            (
                &prod,
                "https://timlee.example/sign-in?returnTo=%2Fsettings&error=link_expired",
                sign_in("/settings", Some("link_expired")),
            ),
            (
                &prod,
                "https://timlee.example/sign-in?returnTo=https://evil.example/",
                sign_in("/apps", None),
            ),
            (
                &prod,
                "https://timlee.example/sign-in?returnTo=//evil.example/",
                sign_in("/apps", None),
            ),
            (
                &prod,
                "https://timlee.example/sign-in?returnTo=/%5Cevil.example",
                sign_in("/apps", None),
            ),
            (
                &prod,
                "https://auth.timlee.example/auth/google/start",
                Route::External,
            ),
            (&prod, "https://github.com/login", Route::External),
            (&prod, "http://timlee.example/apps", Route::Refused),
            (
                &prod,
                "https://timlee.example:8443/apps",
                Route::External,
            ),
            (&prod, "file:///etc/passwd", Route::Refused),
            (&prod, "mailto:security@timlee.example", Route::Refused),
            (&prod, "javascript:alert(1)", Route::Refused),
            (&prod, "tauri://localhost/index.html", Route::InApp),
            (
                &prod,
                "http://tauri.localhost/signin.html?error=timeout",
                Route::InApp,
            ),
            (&prod, "about:blank", Route::InApp),
            (&dev, "http://localhost:19822/apps", Route::InApp),
            (&dev, "http://localhost:19822/", Route::Home),
            (
                &dev,
                "http://localhost:19822/sign-in?desktop=1&port=51234&flow=f",
                Route::BrowserSignIn,
            ),
            (&dev, "http://127.0.0.1:19822/apps", Route::Refused),
            (&dev, "http://localhost:19821/v1/models", Route::Refused),
            // The site in another language (docs/I18N.md §2): the same rules
            // after the locale segment.
            (&prod, "https://timlee.example/ar/", Route::Home),
            (&prod, "https://timlee.example/de", Route::Home),
            (
                &prod,
                "https://timlee.example/ar/sign-in",
                sign_in("/apps", None),
            ),
            (
                &prod,
                "https://timlee.example/ar/sign-in?returnTo=%2Far%2Fmodels&error=link_expired",
                sign_in("/ar/models", Some("link_expired")),
            ),
            (
                &prod,
                "https://timlee.example/pt-br/sign-in?desktop=1&port=51234&code_challenge=abc&code_challenge_method=S256&flow=f",
                Route::BrowserSignIn,
            ),
            (
                &prod,
                "https://timlee.example/ar/sign-in/desktop/start?port=51234&code_challenge=abc&code_challenge_method=S256",
                Route::InApp,
            ),
            (
                &prod,
                "https://timlee.example/ar/sign-in/desktop/complete?code=c",
                Route::InApp,
            ),
            (&prod, "https://timlee.example/ar-xb/sign-in", sign_in("/apps", None)),
            (&prod, "https://timlee.example/de/models", Route::InApp),
            (&dev, "http://localhost:19822/ar/", Route::Home),
            // Not locale segments: the site lowercases one before it means
            // anything, and real paths keep their routes.
            (&prod, "https://timlee.example/DE/sign-in", Route::InApp),
            (&prod, "https://timlee.example/docs/sign-in", Route::InApp),
            (&prod, "https://timlee.example/v1/", Route::InApp),
        ];
        for (origin, target, want) in cases {
            assert_eq!(route(&url(target), origin), want, "{target}");
        }
    }

    #[test]
    fn strips_only_a_locale_segment() {
        for (path, want) in [
            ("/", "/"),
            ("/de", "/"),
            ("/de/", "/"),
            ("/de/sign-in", "/sign-in"),
            ("/pt-br/models", "/models"),
            ("/zh-hant/docs/api", "/docs/api"),
            ("/sign-in", "/sign-in"),
            ("/apps", "/apps"),
            ("/settings/keys", "/settings/keys"),
            ("/abcd/sign-in", "/abcd/sign-in"),
            ("/d/sign-in", "/d/sign-in"),
            ("/de-/sign-in", "/de-/sign-in"),
            ("/de-x/sign-in", "/de-x/sign-in"),
            ("/en_GB/sign-in", "/en_GB/sign-in"),
        ] {
            assert_eq!(strip_locale(path), want, "{path}");
        }
    }

    #[test]
    fn opens_the_browser_sign_in_in_the_site_language() {
        let page = url("https://timlee.example/sign-in?desktop=1&port=51234&flow=f");
        assert_eq!(
            with_locale(&page, "de").as_str(),
            "https://timlee.example/de/sign-in?desktop=1&port=51234&flow=f"
        );
        assert_eq!(
            with_locale(&url("http://localhost:19822/sign-in?desktop=1"), "pt-br").as_str(),
            "http://localhost:19822/pt-br/sign-in?desktop=1"
        );
        // English has no segment; a page that names a language keeps it.
        assert_eq!(with_locale(&page, "en"), page);
        let arabic = url("https://timlee.example/ar/sign-in?desktop=1");
        assert_eq!(with_locale(&arabic, "de"), arabic);
        // The cookie's value goes into a path only when it is a locale ID.
        for hostile in ["../evil", "de/x", "//evil.example", "DE", "", "de?x=1"] {
            assert_eq!(with_locale(&page, hostile), page, "{hostile:?}");
        }
    }

    #[test]
    fn opens_only_https_in_the_browser() {
        let prod = url("https://timlee.example");
        let dev = url("http://localhost:19822");
        assert!(may_open_in_browser(&url("https://example.com/a"), &prod));
        assert!(may_open_in_browser(
            &url("https://timlee.example/sign-in?desktop=1"),
            &prod
        ));
        // A development build signs in against its local stack over http.
        assert!(may_open_in_browser(
            &url("http://localhost:19822/sign-in?desktop=1"),
            &dev
        ));
        for refused in [
            "http://example.com/",
            "http://localhost:19822/sign-in",
            "file:///Applications/Calculator.app",
            "mailto:someone@example.com",
            "ftp://example.com/",
            "smb://server/share",
            "x-apple.systempreferences:com.apple.preference",
        ] {
            assert!(!may_open_in_browser(&url(refused), &prod), "{refused}");
        }
        assert!(!may_open_in_browser(&url("http://127.0.0.1:19822/"), &dev));
    }
}
