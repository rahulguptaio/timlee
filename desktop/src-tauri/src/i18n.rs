//! The app's own words in the reader's language (docs/I18N.md §2, "Desktop
//! shell locale"): the native menus, the update dialogs, the pages bundled
//! from ../src, and the page the browser shows once it has handed a sign-in
//! back to the app.
//!
//! The site's language comes first when the app knows it: the site keeps the
//! reader's choice, and their account's, in the `timlee.locale` cookie of the
//! app's window. Until there is one, the app takes the first of the system's
//! preferred languages that it has a catalog for, then English.
//!
//! The catalogs are the ARB files in ../locales, compiled in. Their messages
//! use the part of the site's ICU MessageFormat subset (docs/I18N.md §3) that
//! the app needs: text and `{name}` arguments. An argument is wrapped in bidi
//! isolates, as the site wraps one, so a version number or a host name keeps
//! its own direction inside a right-to-left sentence.

use std::collections::{BTreeMap, HashMap};
use std::sync::OnceLock;

/// The cookie the site keeps the reader's language in.
pub const COOKIE: &str = "timlee.locale";

const FIRST_STRONG_ISOLATE: char = '\u{2068}';
const POP_DIRECTIONAL_ISOLATE: char = '\u{2069}';

/// The bundled pages' messages, by the first segment of their keys.
const PAGES: [&str; 2] = ["boot.", "signIn."];

/// A language the app ships.
#[derive(Debug)]
pub struct Locale {
    /// The site's ID for it: its URL segment and the cookie's value.
    pub id: &'static str,
    /// For `lang`, and the localization Info.plist declares.
    pub lang: &'static str,
    /// `ltr` or `rtl`, for `dir`: from this table, never guessed from a tag.
    pub dir: &'static str,
    /// The `Intl` locale the pages format times with. English formats as
    /// en-GB, as on the site.
    pub intl: &'static str,
    source: &'static str,
    messages: OnceLock<HashMap<String, String>>,
}

/// The languages the app ships, the site's default first. A language goes in
/// only once the site serves it, because the app sends the browser to the
/// site's sign-in page in that language.
pub static LOCALES: [Locale; 1] = [Locale::new(
    "en",
    "en",
    "ltr",
    "en-GB",
    include_str!("../locales/en.arb"),
)];

/// English, the site's default and every catalog's fallback.
pub fn default() -> &'static Locale {
    &LOCALES[0]
}

impl Locale {
    const fn new(
        id: &'static str,
        lang: &'static str,
        dir: &'static str,
        intl: &'static str,
        source: &'static str,
    ) -> Self {
        Self {
            id,
            lang,
            dir,
            intl,
            source,
            messages: OnceLock::new(),
        }
    }

    /// `key`'s message with its arguments filled in. A message this catalog
    /// lacks falls back to English, and one English lacks shows as its key,
    /// so a mistake is visible rather than blank.
    pub fn format(&self, key: &str, args: &[(&str, &str)]) -> String {
        match self
            .messages()
            .get(key)
            .or_else(|| default().messages().get(key))
        {
            Some(message) => fill(message, args),
            None => key.to_string(),
        }
    }

    pub fn text(&self, key: &str) -> String {
        self.format(key, &[])
    }

    /// The bundled pages' messages, as written: the pages fill in their own
    /// arguments (../src/locale.js).
    pub fn page_messages(&self) -> BTreeMap<String, String> {
        default()
            .messages()
            .iter()
            .filter(|(key, _)| PAGES.iter().any(|page| key.starts_with(page)))
            .map(|(key, english)| {
                let message = self.messages().get(key).unwrap_or(english);
                (key.clone(), message.clone())
            })
            .collect()
    }

    fn messages(&self) -> &HashMap<String, String> {
        self.messages
            .get_or_init(|| parse(self.source).expect("a catalog is valid ARB (see the tests)"))
    }
}

/// The language of the app's own words, among `shipped`: the site's, else the
/// first of the system's that has a catalog, else the first of `shipped`. A
/// site language without a catalog falls back to its parent and then to the
/// first, not to the system's: the site's is the one the reader chose.
pub fn choose<'a>(shipped: &'a [Locale], site: Option<&str>, system: &[String]) -> &'a Locale {
    let found = match site {
        Some(site) => lookup(shipped, site),
        None => system.iter().find_map(|tag| lookup(shipped, tag)),
    };
    found.unwrap_or(&shipped[0])
}

/// The system's preferred languages, most preferred first, as tags such as
/// "de-DE" (macOS: `CFLocaleCopyPreferredLanguages`; Windows:
/// `GetUserPreferredUILanguages`).
pub fn system_languages() -> Vec<String> {
    sys_locale::get_locales().collect()
}

/// The shipped locale for a tag such as "de", "de-DE", "pt_BR" or
/// "zh-Hant-TW": the tag, then the tag without its last subtag, and so on
/// (RFC 4647 §3.4).
fn lookup<'a>(shipped: &'a [Locale], tag: &str) -> Option<&'a Locale> {
    let mut tag = tag.trim().replace('_', "-").to_ascii_lowercase();
    loop {
        if let Some(locale) = shipped.iter().find(|locale| locale.id == tag) {
            return Some(locale);
        }
        tag.truncate(tag.rfind('-')?);
    }
}

/// Fills `{name}` arguments in, each between bidi isolates.
fn fill(message: &str, args: &[(&str, &str)]) -> String {
    let mut out = String::with_capacity(message.len());
    let mut rest = message;
    while let Some(open) = rest.find('{') {
        let Some(close) = rest[open..].find('}').map(|close| open + close) else {
            break;
        };
        out.push_str(&rest[..open]);
        let name = &rest[open + 1..close];
        match args.iter().find(|(arg, _)| *arg == name) {
            Some((_, value)) => {
                out.push(FIRST_STRONG_ISOLATE);
                out.push_str(value);
                out.push(POP_DIRECTIONAL_ISOLATE);
            }
            None => out.push_str(&rest[open..=close]),
        }
        rest = &rest[close + 1..];
    }
    out.push_str(rest);
    out
}

/// An ARB file's messages: its keys that are not metadata (`@…`).
fn parse(source: &str) -> Result<HashMap<String, String>, String> {
    let file: serde_json::Map<String, serde_json::Value> =
        serde_json::from_str(source).map_err(|error| error.to_string())?;
    file.into_iter()
        .filter(|(key, _)| !key.starts_with('@'))
        .map(|(key, value)| match value {
            serde_json::Value::String(message) => Ok((key, message)),
            _ => Err(format!("{key} is not a string")),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeSet;

    /// Where the app names messages: its Rust (this file aside, whose tests
    /// name keys of their own) and the bundled pages.
    const SOURCES: [&str; 9] = [
        "src/lib.rs",
        "src/menu.rs",
        "src/routes.rs",
        "src/signin.rs",
        "src/updates.rs",
        "../src/index.html",
        "../src/signin.html",
        "../src/boot.js",
        "../src/signin.js",
    ];
    const SURFACES: [&str; 5] = ["menu.", "update.", "boot.", "signIn.", "callback."];

    fn read(path: &str) -> String {
        std::fs::read_to_string(std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(path))
            .unwrap_or_else(|error| panic!("{path}: {error}"))
    }

    fn arb(path: &str) -> serde_json::Map<String, serde_json::Value> {
        serde_json::from_str(&read(path)).unwrap()
    }

    /// `[a-z][a-zA-Z0-9]*`: a segment of a key, or an argument's name.
    fn is_name(segment: &str) -> bool {
        segment.starts_with(|c: char| c.is_ascii_lowercase())
            && segment.chars().all(|c| c.is_ascii_alphanumeric())
    }

    /// `<surface>.<area>.<item>[.<variant>]` (§3).
    fn is_key(candidate: &str) -> bool {
        candidate.split('.').count() >= 3 && candidate.split('.').all(is_name)
    }

    /// Every quoted string in `source` that is a message key.
    fn quoted_keys(source: &str) -> BTreeSet<String> {
        let mut keys = BTreeSet::new();
        for (quote, _) in source.match_indices(['"', '\'']) {
            let rest = &source[quote + 1..];
            let end = rest
                .find(|c: char| !(c.is_ascii_alphanumeric() || c == '.'))
                .unwrap_or(rest.len());
            let candidate = &rest[..end];
            if rest[end..].starts_with(['"', '\''])
                && SURFACES
                    .iter()
                    .any(|surface| candidate.starts_with(surface))
                && is_key(candidate)
            {
                keys.insert(candidate.to_string());
            }
        }
        keys
    }

    /// A message's argument names, or why it is not text and `{name}`
    /// arguments only. An ASCII apostrophe is refused: ICU reads it as
    /// quoting, and the catalogs use the typographic one.
    fn arguments(message: &str) -> Result<BTreeSet<String>, String> {
        if message.contains('\'') {
            return Err("an ASCII apostrophe; write ’".into());
        }
        let mut names = BTreeSet::new();
        let mut rest = message;
        while let Some(open) = rest.find(['{', '}']) {
            let tail = &rest[open..];
            let close = tail.find('}').filter(|_| tail.starts_with('{'));
            let Some(close) = close else {
                return Err(format!("a stray brace in {message:?}"));
            };
            let name = &tail[1..close];
            if !is_name(name) {
                return Err(format!("{{{name}}} is not a plain argument"));
            }
            names.insert(name.to_string());
            rest = &tail[close + 1..];
        }
        Ok(names)
    }

    fn locale(id: &'static str, source: &'static str) -> Locale {
        Locale::new(id, id, "ltr", id, source)
    }

    #[test]
    fn fills_arguments_between_bidi_isolates() {
        assert_eq!(
            fill(
                "Restart to Update Timlee {version}",
                &[("version", "0.2.0")]
            ),
            "Restart to Update Timlee \u{2068}0.2.0\u{2069}"
        );
        assert_eq!(
            fill("{a} and {b}.", &[("b", "B"), ("a", "A")]),
            "\u{2068}A\u{2069} and \u{2068}B\u{2069}."
        );
        // An argument nobody passed, and a brace that never closes, stay as written.
        assert_eq!(fill("Hi {name}", &[]), "Hi {name}");
        assert_eq!(fill("Hi {name", &[("name", "x")]), "Hi {name");
        assert_eq!(
            default().format("update.current.message", &[("version", "0.1.0")]),
            "Version \u{2068}0.1.0\u{2069} is the newest version."
        );
    }

    #[test]
    fn falls_back_to_english_then_to_the_key() {
        let german = locale(
            "de",
            r#"{"@@locale": "de", "menu.app.signOut": "Abmelden"}"#,
        );
        assert_eq!(german.text("menu.app.signOut"), "Abmelden");
        assert_eq!(german.text("menu.app.quit"), "Quit Timlee");
        assert_eq!(german.text("menu.app.nothing"), "menu.app.nothing");
        let pages = german.page_messages();
        assert_eq!(pages["signIn.start.button"], "Sign in with browser");
        assert!(pages
            .keys()
            .all(|key| key.starts_with("boot.") || key.starts_with("signIn.")));
        assert!(pages.contains_key("boot.offline.heading"));
    }

    #[test]
    fn chooses_the_site_language_then_the_system_then_the_default() {
        let shipped = [
            locale("en", "{}"),
            locale("de", "{}"),
            locale("ar", "{}"),
            locale("pt-br", "{}"),
        ];
        let choose = |site: Option<&str>, system: &[&str]| {
            let system: Vec<String> = system.iter().map(|tag| tag.to_string()).collect();
            choose(&shipped, site, &system).id
        };
        // The system's languages in its order of preference, region or not.
        assert_eq!(choose(None, &["de-DE", "en-US"]), "de");
        assert_eq!(choose(None, &["fr-FR", "en-US", "de-DE"]), "en");
        assert_eq!(choose(None, &["zh-Hant-TW", "ar-EG"]), "ar");
        assert_eq!(choose(None, &["pt_BR"]), "pt-br");
        assert_eq!(choose(None, &["pt-PT", "fr"]), "en");
        assert_eq!(choose(None, &[]), "en");
        // The site's language wins whatever the system says.
        assert_eq!(choose(Some("de"), &["ar-EG"]), "de");
        assert_eq!(choose(Some("ar-xb"), &["de-DE"]), "ar");
        // One without a catalog is the reader's choice all the same: the
        // default, not the system's.
        assert_eq!(choose(Some("ja"), &["de-DE"]), "en");
    }

    #[test]
    fn every_catalog_has_the_english_messages_and_arguments() {
        let english = arb("locales/en.arb");
        for locale in &LOCALES {
            let messages =
                parse(locale.source).unwrap_or_else(|error| panic!("{}: {error}", locale.id));
            let expected: BTreeSet<&String> =
                english.keys().filter(|key| !key.starts_with('@')).collect();
            assert_eq!(
                messages.keys().collect::<BTreeSet<_>>(),
                expected,
                "{}",
                locale.id
            );
            for (key, message) in &messages {
                let english = english[key].as_str().unwrap();
                assert_eq!(
                    arguments(message)
                        .unwrap_or_else(|error| panic!("{}: {key}: {error}", locale.id)),
                    arguments(english).unwrap(),
                    "{}: {key}",
                    locale.id
                );
            }
        }
        // A translator gets a description of every message (§3).
        for key in english.keys().filter(|key| !key.starts_with('@')) {
            assert!(
                is_key(key) && SURFACES.iter().any(|surface| key.starts_with(surface)),
                "{key}"
            );
            let description = english
                .get(&format!("@{key}"))
                .and_then(|meta| meta["description"].as_str());
            assert!(
                description.is_some_and(|text| !text.is_empty()),
                "{key} has no description"
            );
        }
    }

    #[test]
    fn the_app_uses_every_message_and_no_others() {
        let catalog: BTreeSet<String> = parse(default().source).unwrap().into_keys().collect();
        let used: BTreeSet<String> = SOURCES
            .iter()
            .flat_map(|path| quoted_keys(&read(path)))
            .collect();
        assert_eq!(
            used.difference(&catalog).collect::<Vec<_>>(),
            Vec::<&String>::new(),
            "used, not in en.arb"
        );
        assert_eq!(
            catalog.difference(&used).collect::<Vec<_>>(),
            Vec::<&String>::new(),
            "in en.arb, never used"
        );
    }

    #[test]
    fn ships_the_sites_default_and_declares_every_language() {
        assert_eq!(default().id, crate::routes::DEFAULT_LOCALE);
        assert_eq!((default().dir, default().intl), ("ltr", "en-GB"));
        // macOS localizes its own menu items and panels only into the
        // languages the bundle declares.
        let plist = read("Info.plist");
        for locale in &LOCALES {
            assert!(
                plist.contains(&format!("<string>{}</string>", locale.lang)),
                "{} missing from Info.plist",
                locale.lang
            );
        }
    }
}
