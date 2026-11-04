use std::process::Command;

/// Commands the bundled pages may call. Declaring them makes tauri-build
/// generate `allow-*` permissions, and from then on a command is reachable only
/// from a window a capability grants it to — so a page loaded from the network
/// cannot reach any of them (see capabilities/local.json).
const COMMANDS: &[&str] = &["boot", "reveal", "sign_in", "cancel_sign_in"];

fn main() {
    // The About panel shows the commit next to the version. CI passes it in;
    // a local build asks git, and an export without history says "unknown".
    // HEAD names a branch, so the branch's ref (loose or packed) is what
    // moves on a commit.
    println!("cargo:rerun-if-env-changed=TIMLEE_DESKTOP_COMMIT");
    for path in [
        "../../.git/HEAD",
        "../../.git/refs/heads",
        "../../.git/packed-refs",
    ] {
        println!("cargo:rerun-if-changed={path}");
    }
    let commit = std::env::var("TIMLEE_DESKTOP_COMMIT")
        .ok()
        .filter(|value| !value.is_empty())
        .or_else(git_commit)
        .unwrap_or_else(|| "unknown".into());
    println!("cargo:rustc-env=TIMLEE_DESKTOP_COMMIT={commit}");

    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("tauri-build failed");
}

fn git_commit() -> Option<String> {
    let out = Command::new("git")
        .args(["rev-parse", "--short=12", "HEAD"])
        .output()
        .ok()?;
    let sha = String::from_utf8(out.stdout).ok()?.trim().to_string();
    (out.status.success() && !sha.is_empty()).then_some(sha)
}
