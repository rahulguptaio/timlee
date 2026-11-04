#!/usr/bin/env bash
#
# Stands in for the system browser during desktop/scripts/smoke-macos.sh.
#
# The app (a development build, via TIMLEE_DESKTOP_BROWSER) runs this with
# the sign-in page the site sent its window to, which carries the window's
# flow id. It does what a person in the browser would: signs in through the
# site's development route, then follows the redirect to the app's loopback
# listener. Nothing here sees the PKCE verifier or the window's flow cookie —
# only the app holds them.
#
# Usage: fake-browser.sh <sign-in URL>     (FAKE_BROWSER_LOG: where to log)
set -euo pipefail

url=$1
log=${FAKE_BROWSER_LOG:-/dev/null}
origin=${url%%/sign-in*}
query=${url#*\?}
param() { tr '&' '\n' <<<"$query" | sed -n "s/^$1=//p" | head -n 1; }
port=$(param port)
challenge=$(param code_challenge)
flow=$(param flow)

echo "browser: opened ${origin}/sign-in for the app on port ${port}" >>"$log"
if [[ -z $flow ]]; then
  echo "browser: the page carries no flow id, so the app did not start at /sign-in/desktop/start" >>"$log"
  exit 1
fi
delivery=$(curl -sS -o /dev/null -w '%{redirect_url}' -H "Origin: ${origin}" \
  --data-urlencode intent=dev --data-urlencode returnTo=/apps \
  --data-urlencode "desktop_port=${port}" --data-urlencode "code_challenge=${challenge}" \
  --data-urlencode "desktop_flow=${flow}" "${origin}/sign-in")
case $delivery in
  "http://127.0.0.1:${port}/callback?code="*) ;;
  *)
    echo "browser: the site did not hand off to the app (redirect: ${delivery%%\?*})" >>"$log"
    exit 1
    ;;
esac
status=$(curl -sS -o /dev/null -w '%{http_code}' "$delivery")
echo "browser: handed the code to 127.0.0.1:${port}, which answered HTTP ${status}" >>"$log"
