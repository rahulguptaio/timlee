#!/usr/bin/env bash
#
# Desktop smoke test (macOS): builds Timlee.app against a running local stack
# and proves it loads, then exercises the behaviour a person relies on.
#
#   window       the release app opens one window, on screen, at a sane size
#   render       the site renders inside it (sign-in when signed out, the
#                console when signed in), reported from the webview's own DOM
#   theme        the bundled pages follow the system appearance
#   navigation   non-https links are refused, never handed to the system
#   single       a second launch exits and brings the first window back
#   close        closing the window hides it; reopening the app shows it again
#   offline      with nothing listening, the bundled offline screen appears
#   sign-in      RFC 8252 end to end: the app's button, the site's start page
#                in the app window (it sets the flow cookie there), a browser
#                sign-in (desktop/scripts/fake-browser.sh), the console signed
#                in
#
# Start the stack first, e.g. from the repository root:
#   go build -o .local/bin/timlee-dev ./cmd/timlee-dev &&
#     .local/bin/timlee-dev -auth-port 19820 -data-port 19821 -web-port 19822
#
# Usage: desktop/scripts/smoke-macos.sh [--skip-build] [--sign-in-only]
#   --skip-build    test the builds already in src-tauri/target
#   --sign-in-only  only the bundle and sign-in checks: one launch of the app
#                   instead of four
# Environment:
#   TIMLEE_SMOKE_ORIGIN       the stack's site     (default http://localhost:19822)
#   TIMLEE_SMOKE_DEAD_ORIGIN  where nothing listens (default http://localhost:19823)
#   TIMLEE_SMOKE_STACK_LOG    the stack's log, to show the app's requests in it
#
# Needs Accessibility and Screen Recording permission for the terminal (System
# Settings → Privacy & Security). Writes everything to .smoke/desktop/. It opens,
# brings forward and closes Timlee windows for about a minute, so run it when
# nobody is working at this Mac, or use --sign-in-only.
# Exit: 0 every check passed, 1 a check failed, 2 a prerequisite is missing.
set -euo pipefail

ROOT=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
DESKTOP=$ROOT/desktop
TAURI=$DESKTOP/src-tauri
ORIGIN=${TIMLEE_SMOKE_ORIGIN:-http://localhost:19822}
DEAD_ORIGIN=${TIMLEE_SMOKE_DEAD_ORIGIN:-http://localhost:19823}
STACK_LOG=${TIMLEE_SMOKE_STACK_LOG:-}
OUT=$ROOT/.smoke/desktop
APP=$TAURI/target/release/bundle/macos/Timlee.app
DEBUG_BIN=$TAURI/target/debug/timlee-desktop
build=1 sign_in_only=0
for arg in "$@"; do
  case $arg in
    --skip-build) build=0 ;;
    --sign-in-only) sign_in_only=1 ;;
    *)
      echo "usage: $0 [--skip-build] [--sign-in-only]" >&2
      exit 2
      ;;
  esac
done

failures=0
pids=()
pass() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() {
  printf '\033[31m✗\033[0m %s\n' "$1"
  failures=$((failures + 1))
}
note() { printf '  %s\n' "$1"; }
missing() {
  printf '\033[31m✗\033[0m %s\n' "$1" >&2
  exit 2
}

cleanup() {
  local pid
  # ${a[@]+...}: bash 3.2, which macOS ships, calls an empty array unbound.
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

# --- Prerequisites ------------------------------------------------------------
[[ $(uname -s) == Darwin ]] || missing "macOS only"
command -v cargo >/dev/null || missing "cargo not found: install Rust (https://rustup.rs)"
[[ -x $DESKTOP/node_modules/.bin/tauri ]] ||
  missing "Tauri CLI missing: run pnpm --dir desktop install --ignore-workspace --frozen-lockfile"
curl -s -o /dev/null --max-time 5 "$ORIGIN/" || missing "no stack at $ORIGIN (see the header of this script)"
if curl -s -o /dev/null --max-time 2 "$DEAD_ORIGIN/"; then
  missing "something answers at $DEAD_ORIGIN; set TIMLEE_SMOKE_DEAD_ORIGIN to a closed port"
fi
mkdir -p "$OUT"
rm -f "$OUT"/*.log "$OUT"/*.png

# --- Helpers ------------------------------------------------------------------

# wait_for FILE REGEX SECONDS [COUNT]: poll a log until COUNT lines (default
# 1) match.
wait_for() {
  local deadline=$((SECONDS + $3)) want=${4:-1}
  while ((SECONDS < deadline)); do
    (($(grep -Ec "$2" "$1" 2>/dev/null || true) >= want)) && return 0
    sleep 0.5
  done
  return 1
}

# seen FILE REGEX: how many lines match so far.
seen() { grep -Ec "$2" "$1" 2>/dev/null || true; }

# windows PID: the on-screen, ordinary windows a process owns, one per line as
# "<window id> <width>x<height>".
windows() {
  osascript -l JavaScript - "$1" <<'JXA'
ObjC.import('CoreGraphics');
function run(argv) {
  const pid = Number(argv[0]);
  const all = ObjC.deepUnwrap(ObjC.castRefToObject(
    $.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly, 0)));
  return all
    .filter((w) => w.kCGWindowOwnerPID === pid && w.kCGWindowLayer === 0)
    .map((w) => `${w.kCGWindowNumber} ${Math.round(w.kCGWindowBounds.Width)}x${Math.round(w.kCGWindowBounds.Height)}`)
    .join('\n');
}
JXA
}

# press PID LABEL: press a button inside the app's webview, as VoiceOver or a
# click would, through the accessibility tree.
press() {
  osascript -l JavaScript - "$1" "$2" <<'JXA'
function run(argv) {
  const [pid, label] = [Number(argv[0]), argv[1]];
  const proc = Application('System Events').processes.whose({ unixId: pid })[0];
  const text = (read) => { try { return read() || ''; } catch (e) { return ''; } };
  function find(element, depth) {
    if (depth > 14) return null;
    let children = [];
    try { children = element.uiElements(); } catch (e) { return null; }
    for (const child of children) {
      if (text(child.role) === 'AXButton' &&
          [text(child.title), text(child.description), text(child.name)].includes(label)) return child;
      const found = find(child, depth + 1);
      if (found) return found;
    }
    return null;
  }
  const button = find(proc.windows[0], 0);
  if (!button) return 'not found';
  button.actions['AXPress'].perform();
  return 'pressed';
}
JXA
}

# menu PID MENU ITEM: choose a native menu item.
menu() {
  osascript -e "tell application \"System Events\" to tell (first process whose unix id is $1) to click menu item \"$3\" of menu 1 of menu bar item \"$2\" of menu bar 1" >/dev/null
}

# front PID: bring the app forward, as a click on its Dock icon would. The app
# never takes focus by itself, and WebKit paints nothing in a window hidden
# behind others, so a screenshot would show only the window's background.
front() {
  osascript -e "tell application \"System Events\" to set frontmost of (first process whose unix id is $1) to true" >/dev/null || true
  sleep 1
}

# close_window PID: the red button.
close_window() {
  osascript -e "tell application \"System Events\" to tell (first process whose unix id is $1) to click (first button of window 1 whose subrole is \"AXCloseButton\")" >/dev/null
}

# shot PID FILE: a picture of the app's window alone, whatever is in front.
shot() {
  local id
  id=$(windows "$1" | awk 'NR == 1 { print $1 }')
  [[ -n $id ]] && screencapture -x -o -l "$id" "$2" && [[ -s $2 ]]
}

# launch NAME BINARY [ENV...]: start the app with page reports on.
launch() {
  local name=$1 binary=$2
  shift 2
  : >"$OUT/$name.log"
  env TIMLEE_DESKTOP_REPORT=1 ${@+"$@"} "$binary" >>"$OUT/$name.log" 2>&1 &
  pids+=($!)
  last_pid=$!
}

stop() {
  kill "$1" 2>/dev/null || true
  wait "$1" 2>/dev/null || true
}

field() { sed -n "s/.*\"$1\":\([^,}]*\).*/\1/p" <<<"$2" | tr -d '"'; }

os_dark=false
[[ $(defaults read -g AppleInterfaceStyle 2>/dev/null || true) == Dark ]] && os_dark=true

printf '\033[1mTimlee desktop smoke test\033[0m — %s\n\n' "$ORIGIN"

# --- Build --------------------------------------------------------------------
if ((build)); then
  echo "building the release app against ${ORIGIN}…"
  (cd "$DESKTOP" && TIMLEE_DESKTOP_ORIGIN=$ORIGIN node_modules/.bin/tauri build --bundles app) \
    >"$OUT/build-release.log" 2>&1 || {
    tail -20 "$OUT/build-release.log"
    missing "release build failed (log: .smoke/desktop/build-release.log)"
  }
  echo "building the development binary against ${DEAD_ORIGIN}…"
  (cd "$TAURI" && TIMLEE_DESKTOP_ORIGIN=$DEAD_ORIGIN cargo build --quiet) \
    >"$OUT/build-offline.log" 2>&1 || missing "development build failed (log: .smoke/desktop/build-offline.log)"
  cp "$DEBUG_BIN" "$OUT/timlee-offline"
  echo "building the development binary against ${ORIGIN}…"
  (cd "$TAURI" && TIMLEE_DESKTOP_ORIGIN=$ORIGIN cargo build --quiet) \
    >"$OUT/build-debug.log" 2>&1 || missing "development build failed (log: .smoke/desktop/build-debug.log)"
  echo
fi
[[ -d $APP ]] || missing "no bundle at $APP; run without --skip-build"

# --- The bundle ---------------------------------------------------------------
plist() { /usr/libexec/PlistBuddy -c "Print :$1" "$APP/Contents/Info.plist" 2>/dev/null; }
BIN=$APP/Contents/MacOS/$(plist CFBundleExecutable)
expect_plist() {
  if [[ $(plist "$1") == "$2" ]]; then pass "Info.plist $1 = $2"; else fail "Info.plist $1 = $(plist "$1"), want $2"; fi
}
expect_plist CFBundleIdentifier io.timlee.desktop
expect_plist CFBundleName Timlee
expect_plist CFBundleExecutable Timlee
expect_plist LSMinimumSystemVersion 12.0
if [[ -s $APP/Contents/Resources/icon.icns ]]; then pass "icon bundled"; else fail "icon.icns missing"; fi
note "size $(du -sh "$APP" | cut -f1); $(codesign -dv "$APP" 2>&1 | grep -E '^(Signature|TeamIdentifier|Authority)' | head -1 || echo 'not signed')"

# --- Window, render, theme, navigation ----------------------------------------
if ((sign_in_only)); then
  note "window, single instance, close and offline checks skipped (--sign-in-only)"
else
  launch release "$BIN" TIMLEE_DESKTOP_SMOKE_NAVIGATE="http://example.com/ mailto:security@timlee.example"
  app=$last_pid
  if wait_for "$OUT/release.log" '^page: .*"url":"(tauri:\\/\\/localhost\\/signin.html|http[^"]*\\/apps)"' 30; then
    page=$(grep -E '^page: ' "$OUT/release.log" | grep -v '"url":"tauri:\\/\\/localhost\\/"' | head -1 || true)
    pass "rendered $(field url "$page" | sed 's/\\//g') — \"$(field heading "$page")\""
  else
    fail "no page rendered within 30 s"
    tail -20 "$OUT/release.log"
  fi
  front "$app"
  if kill -0 "$app" 2>/dev/null; then pass "process alive (pid $app)"; else fail "process exited"; fi
  geometry=$(windows "$app" | awk 'NR == 1 { print $2 }')
  if [[ $geometry =~ ^([0-9]+)x([0-9]+)$ ]] && ((BASH_REMATCH[1] >= 900 && BASH_REMATCH[2] >= 600)); then
    pass "one window on screen, $geometry"
  else
    fail "no on-screen window of at least 900x600 (got '${geometry:-none}')"
  fi
  if [[ $(field prefersDark "${page:-}") == "$os_dark" ]]; then
    pass "webview sees the system appearance (dark: $os_dark)"
  else
    fail "webview prefers-color-scheme dark=$(field prefersDark "${page:-}"), system dark=$os_dark"
  fi
  if shot "$app" "$OUT/release-window.png"; then pass "screenshot .smoke/desktop/release-window.png"; else fail "screenshot failed"; fi
  if wait_for "$OUT/release.log" '^navigation: refused http://example.com/' 15 &&
    wait_for "$OUT/release.log" '^navigation: refused mailto:security@timlee.example' 5; then
    pass "non-https links refused (http://example.com/, mailto:)"
  else
    fail "a non-https link was not refused"
  fi
  if grep -q '^navigation: browser' "$OUT/release.log"; then fail "something was handed to the system browser"; fi

  # --- Single instance --------------------------------------------------------
  second_start=$SECONDS
  if "$BIN" >"$OUT/second.log" 2>&1; then second_status=0; else second_status=$?; fi
  if ((second_status == 0 && SECONDS - second_start < 10)) &&
    wait_for "$OUT/release.log" '^single-instance: another launch handed over' 5; then
    pass "second launch handed over and exited ($((SECONDS - second_start)) s)"
  else
    fail "second launch: exit $second_status after $((SECONDS - second_start)) s"
  fi
  running=$(pgrep -f "$BIN" | wc -l | tr -d ' ' || true)
  if [[ $running == 1 ]]; then pass "exactly one Timlee process"; else fail "$running Timlee processes"; fi

  # --- Close hides, reopening shows -------------------------------------------
  close_window "$app" && sleep 1
  if [[ -z $(windows "$app") ]] && kill -0 "$app" 2>/dev/null; then
    pass "closing the window hides it; the app keeps running"
  else
    fail "closing the window did not hide it"
  fi
  open "$APP" && sleep 2
  if [[ -n $(windows "$app") ]]; then pass "reopening the app shows the window again"; else fail "the window did not come back"; fi
  stop "$app"

  # --- Offline ----------------------------------------------------------------
  if [[ -x $OUT/timlee-offline ]]; then
    launch offline "$OUT/timlee-offline"
    # The heading has a typographic apostrophe: "Timlee can’t reach …".
    offline_heading='"heading":"Timlee can.{1,3}t reach'
    if wait_for "$OUT/offline.log" "$offline_heading" 30; then
      pass "offline screen: \"$(field heading "$(grep -E "$offline_heading" "$OUT/offline.log" | head -1)")\""
      front "$last_pid"
      shot "$last_pid" "$OUT/offline-window.png" && note "screenshot .smoke/desktop/offline-window.png"
    else
      fail "the offline screen did not appear"
    fi
    stop "$last_pid"
  else
    note "offline check skipped: no development build (run without --skip-build)"
  fi
fi

# --- Sign-in, end to end ------------------------------------------------------
supported=$(curl -s -o /dev/null -w '%{http_code}' "$ORIGIN/sign-in/desktop/start")
if [[ $supported == 404 ]]; then
  note "sign-in check skipped: $ORIGIN has no /sign-in/desktop/start (an older site)"
elif [[ ! -x $DEBUG_BIN ]]; then
  note "sign-in check skipped: no development build"
else
  : >"$OUT/browser.log"
  launch signin "$DEBUG_BIN" TIMLEE_DESKTOP_BROWSER="$DESKTOP/scripts/fake-browser.sh" FAKE_BROWSER_LOG="$OUT/browser.log"
  app=$last_pid
  wait_for "$OUT/signin.log" '^page: .*"url":"(tauri:\\/\\/localhost\\/signin.html|http[^"]*\\/apps)"' 30 || true
  if grep -q '"signedIn":true' "$OUT/signin.log"; then
    # Left signed in by an earlier run: start from the sign-in screen.
    menu "$app" Timlee "Sign Out"
    wait_for "$OUT/signin.log" '^page: .*signin.html' 15 || true
    sleep 1
  fi
  page=$(grep -E '^page: .*"url":"tauri:\\/\\/localhost\\/signin.html"' "$OUT/signin.log" | tail -1 || true)
  if [[ -n $page ]]; then pass "showed the sign-in screen: \"$(field heading "$page")\""; else fail "the sign-in screen did not render"; fi
  front "$app"
  shot "$app" "$OUT/signin-window.png" && note "screenshot .smoke/desktop/signin-window.png"
  signed_in=$(seen "$OUT/signin.log" '^page: .*"signedIn":true')
  if [[ $(press "$app" "Sign in with browser") == pressed ]] &&
    wait_for "$OUT/signin.log" '^page: .*"signedIn":true' 30 $((signed_in + 1)); then
    console=$(grep '"signedIn":true' "$OUT/signin.log" | tail -1)
    pass "signed in through the browser: $(field url "$console" | sed 's/\\//g') — \"$(field title "$console")\""
    sed 's/^/  /' "$OUT/browser.log"
    note "the site chose data-theme=$(field dataTheme "$console") with the system dark=$os_dark"
    front "$app"
    shot "$app" "$OUT/apps-window.png" && note "screenshot .smoke/desktop/apps-window.png"
    signed_out=$(seen "$OUT/signin.log" '^navigation: sign-in')
    menu "$app" Timlee "Sign Out"
    if wait_for "$OUT/signin.log" '^navigation: sign-in' 15 $((signed_out + 1)); then
      pass "Sign Out (menu) returns to the sign-in screen"
    else
      fail "Sign Out did not sign out"
    fi
  else
    fail "the browser sign-in did not reach the console"
    tail -5 "$OUT/signin.log"
    cat "$OUT/browser.log"
  fi
  stop "$app"
fi

# --- Panics and the stack's view ----------------------------------------------
# -s: a check that did not run left no log.
if grep -qsiE 'panicked|fatal' "$OUT"/{release,second,offline,signin}.log; then
  fail "a panic was logged"
else
  pass "no panics logged"
fi
if [[ -n $STACK_LOG && -f $STACK_LOG ]]; then
  note "the stack saw (last 8 site requests):"
  { grep -E '"msg":"request","service":"timlee-web"' "$STACK_LOG" || true; } | tail -8 |
    sed -E 's/.*"method":"([A-Z]+)","path":"([^"]*)","status":([0-9]+).*/    \1 \2 \3/'
fi

echo
if ((failures == 0)); then
  printf '\033[32mDesktop smoke test passed.\033[0m Logs and screenshots: .smoke/desktop/\n'
else
  printf '\033[31mDesktop smoke test: %d check(s) failed.\033[0m Logs: .smoke/desktop/\n' "$failures"
  exit 1
fi
