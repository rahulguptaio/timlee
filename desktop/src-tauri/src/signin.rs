//! Signing in through the system browser (RFC 8252).
//!
//! The app never shows an identity provider's page in its own webview — Google
//! refuses embedded user agents, and a native app painting a password field
//! is indistinguishable from one stealing it (RFC 8252 §8.12). Instead:
//!
//! 1. bind a listener on 127.0.0.1 with an ephemeral port (§7.3) and make a
//!    PKCE verifier and S256 challenge (RFC 7636);
//! 2. open `<origin>/sign-in/desktop/start?port=…&code_challenge=…` in the
//!    app window. The site sets a flow cookie there and redirects the window
//!    to its sign-in page with the same flow id, which the app opens in the
//!    system browser instead (`routes`); the reader signs in there however
//!    they normally do;
//! 3. the server redirects the browser to `http://127.0.0.1:<port>/callback`
//!    with a one-time code bound to the challenge and the flow;
//! 4. the app window opens `<origin>/sign-in/desktop/complete` with the code
//!    and the verifier. The server checks one against the other and the
//!    window's flow cookie against the code, and only then sets the session
//!    cookie — in the app webview's own cookie jar.
//!
//! A process that wins the race for the port receives a code it cannot spend:
//! the verifier never leaves this process except to the server over TLS. And
//! only the window that started a sign-in can finish it, so nobody can finish
//! a sign-in to their own account in somebody else's browser.

use std::io::{Read, Write};
use std::net::{Ipv4Addr, SocketAddr, TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
use sha2::{Digest, Sha256};
use tauri::Url;

use crate::i18n::Locale;

/// How long a started sign-in waits for the browser. A mailed sign-in link
/// stays valid for 15 minutes, so the listener does too.
pub const WAIT: Duration = Duration::from_secs(15 * 60);

/// A PKCE pair (RFC 7636 §4.1–4.2).
pub struct Pkce {
    pub verifier: String,
    pub challenge: String,
}

impl Pkce {
    /// 32 random bytes give the 43-character verifier the server expects.
    pub fn generate() -> std::io::Result<Self> {
        let mut bytes = [0u8; 32];
        getrandom::fill(&mut bytes).map_err(|error| std::io::Error::other(error.to_string()))?;
        Ok(Self::from_verifier(URL_SAFE_NO_PAD.encode(bytes)))
    }

    fn from_verifier(verifier: String) -> Self {
        let challenge = URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()));
        Self {
            verifier,
            challenge,
        }
    }
}

/// Where the app window starts a sign-in. The site answers with the flow
/// cookie and a redirect to the sign-in page the system browser opens.
pub fn start_url(origin: &Url, port: u16, challenge: &str, return_to: &str) -> Url {
    let mut url = origin
        .join("/sign-in/desktop/start")
        .expect("a static path joins any origin");
    url.query_pairs_mut()
        .append_pair("port", &port.to_string())
        .append_pair("code_challenge", challenge)
        .append_pair("code_challenge_method", "S256")
        .append_pair("returnTo", return_to);
    url
}

/// Where the app window redeems the code for a session.
pub fn completion_url(origin: &Url, code: &str, verifier: &str, return_to: &str) -> Url {
    let mut url = origin
        .join("/sign-in/desktop/complete")
        .expect("a static path joins any origin");
    url.query_pairs_mut()
        .append_pair("code", code)
        .append_pair("code_verifier", verifier)
        .append_pair("returnTo", return_to);
    url
}

/// How a sign-in that was not cancelled ended.
pub enum Done {
    /// The browser delivered a code; open this URL in the app window.
    Completed(Url),
    TimedOut,
    Failed(std::io::Error),
}

/// The sign-in in progress, if any. Starting another cancels it, so a reader
/// who clicks twice gets one browser tab that works and one that is stale,
/// never two listeners.
#[derive(Default)]
pub struct SignIn {
    pending: Mutex<Option<Arc<AtomicBool>>>,
}

impl SignIn {
    /// Binds the listener and starts waiting on a background thread. Returns
    /// the URL for the app window (`start_url`); `done` runs once, off the
    /// main thread, unless the sign-in is cancelled first. `page` is what the
    /// browser shows once it has handed the code over (`return_page`).
    pub fn start(
        &self,
        origin: &Url,
        return_to: &str,
        page: String,
        done: impl FnOnce(Done) + Send + 'static,
    ) -> std::io::Result<Url> {
        let listener = TcpListener::bind(SocketAddr::from((Ipv4Addr::LOCALHOST, 0)))?;
        let port = listener.local_addr()?.port();
        let pkce = Pkce::generate()?;
        let start = start_url(origin, port, &pkce.challenge, return_to);

        let cancel = Arc::new(AtomicBool::new(false));
        if let Some(previous) = self.pending.lock().unwrap().replace(cancel.clone()) {
            previous.store(true, Ordering::SeqCst);
        }

        let (origin, return_to) = (origin.clone(), return_to.to_string());
        std::thread::spawn(move || {
            match wait_for_code(listener, &page, &cancel, Instant::now() + WAIT) {
                Ok(Some(code)) => done(Done::Completed(completion_url(
                    &origin,
                    &code,
                    &pkce.verifier,
                    &return_to,
                ))),
                Ok(None) if cancel.load(Ordering::SeqCst) => {}
                Ok(None) => done(Done::TimedOut),
                Err(error) => done(Done::Failed(error)),
            }
        });
        Ok(start)
    }

    pub fn cancel(&self) {
        if let Some(pending) = self.pending.lock().unwrap().take() {
            pending.store(true, Ordering::SeqCst);
        }
    }
}

/// Accepts connections until one delivers a code, the sign-in is cancelled,
/// or the deadline passes (`Ok(None)` for both of the last two). The one that
/// delivers it gets `page`.
pub fn wait_for_code(
    listener: TcpListener,
    page: &str,
    cancel: &AtomicBool,
    deadline: Instant,
) -> std::io::Result<Option<String>> {
    // Polling keeps cancellation simple: no second socket to wake a blocked
    // accept, and the port is released the moment this returns.
    listener.set_nonblocking(true)?;
    while !cancel.load(Ordering::SeqCst) && Instant::now() < deadline {
        match listener.accept() {
            // Bound to 127.0.0.1, so the peer is always local; checked anyway
            // because it costs nothing.
            Ok((stream, peer)) if peer.ip().is_loopback() => {
                if let Some(code) = answer(stream, page) {
                    return Ok(Some(code));
                }
            }
            Ok(_) => {}
            Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => {}
            Err(error) => return Err(error),
        }
    }
    Ok(None)
}

/// Reads one request and answers it. Only `GET /callback?code=…` yields a
/// code; anything else — a favicon fetch, a port scan — gets a 404 and the
/// wait goes on, so it cannot use up the one callback.
fn answer(mut stream: TcpStream, page: &str) -> Option<String> {
    stream.set_nonblocking(false).ok()?;
    stream.set_read_timeout(Some(Duration::from_secs(5))).ok()?;
    stream
        .set_write_timeout(Some(Duration::from_secs(5)))
        .ok()?;

    let mut request = Vec::with_capacity(1024);
    let mut chunk = [0u8; 1024];
    while request.len() < 8192 && !request.windows(4).any(|w| w == b"\r\n\r\n") {
        match stream.read(&mut chunk) {
            Ok(0) | Err(_) => break,
            Ok(n) => request.extend_from_slice(&chunk[..n]),
        }
    }

    let code = parse_callback(&request);
    let (status, body) = match code {
        Some(_) => ("200 OK", page),
        None => ("404 Not Found", "Not found\n"),
    };
    // The page carries no script and loads nothing, and the code in its URL
    // must not travel on in a Referer or sit in a disk cache.
    let response = format!(
        "HTTP/1.1 {status}\r\n\
         Content-Type: text/html; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Cache-Control: no-store\r\n\
         Referrer-Policy: no-referrer\r\n\
         Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'\r\n\
         X-Content-Type-Options: nosniff\r\n\
         Connection: close\r\n\r\n{body}",
        body.len()
    );
    let _ = stream.write_all(response.as_bytes());
    let _ = stream.flush();
    code
}

fn parse_callback(request: &[u8]) -> Option<String> {
    let line = std::str::from_utf8(request).ok()?.lines().next()?;
    let mut parts = line.split(' ');
    let (method, target) = (parts.next()?, parts.next()?);
    if method != "GET" || !target.starts_with('/') {
        return None;
    }
    let url = Url::parse("http://127.0.0.1").ok()?.join(target).ok()?;
    if url.path() != "/callback" {
        return None;
    }
    url.query_pairs()
        .find(|(key, _)| key == "code")
        .map(|(_, value)| value.into_owned())
        .filter(|code| !code.is_empty() && code.len() <= 512)
}

/// What the browser tab shows once it has handed the code over, in the app's
/// language. The sign-in is not finished until the app has redeemed it, so it
/// says where to look rather than claiming success.
pub fn return_page(locale: &Locale) -> String {
    format!(
        r#"<!doctype html>
<html lang="{lang}" dir="{dir}">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{title}</title>
<style>
  body {{ margin: 0; min-height: 100vh; display: grid; place-items: center;
         font: 1rem/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }}
  main {{ max-width: 30rem; padding: 2rem; }}
  h1 {{ font-size: 1.5rem; margin: 0 0 0.5rem; }}
</style>
<main>
  <h1>{heading}</h1>
  <p>{body}</p>
</main>
"#,
        lang = locale.lang,
        dir = locale.dir,
        title = escape_html(&locale.text("callback.page.title")),
        heading = escape_html(&locale.text("callback.page.heading")),
        body = escape_html(&locale.text("callback.page.body")),
    )
}

fn escape_html(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::TcpStream;

    #[test]
    fn derives_the_rfc_7636_challenge() {
        // RFC 7636 Appendix B.
        let pkce = Pkce::from_verifier("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk".into());
        assert_eq!(
            pkce.challenge,
            "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
        );
    }

    #[test]
    fn generates_verifiers_the_server_accepts() {
        let (a, b) = (Pkce::generate().unwrap(), Pkce::generate().unwrap());
        assert_eq!(a.verifier.len(), 43);
        assert_eq!(a.challenge.len(), 43);
        assert_ne!(a.verifier, b.verifier);
        let unreserved = |s: &str| {
            s.bytes()
                .all(|c| c.is_ascii_alphanumeric() || c == b'-' || c == b'_')
        };
        assert!(unreserved(&a.verifier) && unreserved(&a.challenge));
    }

    #[test]
    fn builds_the_start_and_completion_urls() {
        let origin = Url::parse("https://timlee.example").unwrap();
        let start = start_url(&origin, 51234, "abc", "/settings");
        assert_eq!(
            start.as_str(),
            "https://timlee.example/sign-in/desktop/start?port=51234&code_challenge=abc&code_challenge_method=S256&returnTo=%2Fsettings"
        );
        let done = completion_url(&origin, "c&d", "v", "/apps");
        assert_eq!(
            done.as_str(),
            "https://timlee.example/sign-in/desktop/complete?code=c%26d&code_verifier=v&returnTo=%2Fapps"
        );
    }

    #[test]
    fn parses_only_the_callback() {
        let parse = |line: &str| parse_callback(format!("{line}\r\nHost: x\r\n\r\n").as_bytes());
        assert_eq!(parse("GET /callback?code=abc HTTP/1.1"), Some("abc".into()));
        assert_eq!(
            parse("GET /callback?state=x&code=a%2Bb HTTP/1.1"),
            Some("a+b".into())
        );
        assert_eq!(parse("GET /callback HTTP/1.1"), None);
        assert_eq!(parse("GET /callback?code= HTTP/1.1"), None);
        assert_eq!(parse("GET /favicon.ico HTTP/1.1"), None);
        assert_eq!(parse("POST /callback?code=abc HTTP/1.1"), None);
        assert_eq!(
            parse("GET http://127.0.0.1/callback?code=abc HTTP/1.1"),
            None
        );
        assert_eq!(
            parse(&format!("GET /callback?code={} HTTP/1.1", "a".repeat(513))),
            None
        );
    }

    fn get(port: u16, target: &str) -> String {
        let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
        write!(stream, "GET {target} HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n").unwrap();
        let mut response = String::new();
        stream.read_to_string(&mut response).unwrap();
        response
    }

    #[test]
    fn waits_past_stray_requests_for_the_code() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let cancel = AtomicBool::new(false);
        let waiter = std::thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(10);
            wait_for_code(listener, "<p>Handed over</p>", &cancel, deadline)
        });

        assert!(get(port, "/favicon.ico").starts_with("HTTP/1.1 404"));
        let page = get(port, "/callback?code=one-time");
        assert!(page.starts_with("HTTP/1.1 200"));
        assert!(page.contains("Cache-Control: no-store"));
        assert!(page.ends_with("\r\n\r\n<p>Handed over</p>"));
        assert_eq!(waiter.join().unwrap().unwrap(), Some("one-time".into()));
        // One code per sign-in: the port is closed once it has been delivered.
        assert!(TcpStream::connect(("127.0.0.1", port)).is_err());
    }

    #[test]
    fn the_return_page_is_in_the_apps_language() {
        let page = return_page(crate::i18n::default());
        assert!(page.contains(r#"<html lang="en" dir="ltr">"#));
        assert!(page.contains("<title>Return to Timlee</title>"));
        assert!(page.contains("<h1>Return to Timlee</h1>"));
        assert!(page.contains("<p>Timlee is finishing your sign-in. You can close this tab.</p>"));
        assert_eq!(
            escape_html(r#"<b>"Tom & Jerry"</b>"#),
            "&lt;b&gt;&quot;Tom &amp; Jerry&quot;&lt;/b&gt;"
        );
    }

    #[test]
    fn stops_on_cancel_and_on_the_deadline() {
        let cancelled = AtomicBool::new(true);
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let far = Instant::now() + Duration::from_secs(60);
        assert_eq!(wait_for_code(listener, "", &cancelled, far).unwrap(), None);

        let waiting = AtomicBool::new(false);
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let started = Instant::now();
        let soon = started + Duration::from_millis(200);
        assert_eq!(wait_for_code(listener, "", &waiting, soon).unwrap(), None);
        assert!(started.elapsed() < Duration::from_secs(5));
    }

    #[test]
    fn a_second_start_cancels_the_first() {
        let origin = Url::parse("http://localhost:1").unwrap();
        let signin = SignIn::default();
        let (tx, rx) = std::sync::mpsc::channel();
        let first_tx = tx.clone();
        let first = signin
            .start(&origin, "/apps", String::new(), move |_| {
                first_tx.send("first").unwrap()
            })
            .unwrap();
        let second = signin
            .start(&origin, "/apps", String::new(), move |_| {
                tx.send("second").unwrap()
            })
            .unwrap();
        assert_ne!(first, second);

        let port_of = |url: &Url| -> u16 {
            url.query_pairs()
                .find(|(k, _)| k == "port")
                .unwrap()
                .1
                .parse()
                .unwrap()
        };
        // The first listener goes away without reporting anything.
        std::thread::sleep(Duration::from_millis(300));
        assert!(TcpStream::connect(("127.0.0.1", port_of(&first))).is_err());
        assert!(get(port_of(&second), "/callback?code=x").starts_with("HTTP/1.1 200"));
        assert_eq!(rx.recv_timeout(Duration::from_secs(5)).unwrap(), "second");
        assert!(rx.recv_timeout(Duration::from_millis(300)).is_err());
    }
}
