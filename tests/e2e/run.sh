#!/bin/sh
# Browser tests with Playwright from nixpkgs: builds focus-server and the
# focus command line, starts the server on a throwaway database and drives
# the app with Chromium (and the CLI). Screenshots go to tests/e2e/out.
set -eu
cd "$(dirname "$0")/../.."
mkdir -p tests/e2e/out
work=$(mktemp -d)
trap 'kill $pid 2>/dev/null || true; rm -rf "$work"' EXIT

nix shell nixpkgs#go --command sh -c "go build -o '$work/focus-server' ./cmd/focus-server && go build -o '$work/focus' ./cmd/focus"
PORT=${PORT:-18765}
TZ=Europe/Paris "$work/focus-server" -port "$PORT" -data-dir "$work/data" >"$work/server.log" 2>&1 &
pid=$!

nix-shell -p 'python3.withPackages (ps: [ ps.playwright ])' --run "
  export PLAYWRIGHT_BROWSERS_PATH=$(nix build --no-link --print-out-paths nixpkgs#playwright-driver.browsers)
  export PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=true
  export BASE=http://127.0.0.1:$PORT OUT=tests/e2e/out TZ=Europe/Paris CLI='$work/focus' HOME='$work/home'
  python3 tests/e2e/smoke.py
" || { cat "$work/server.log"; exit 1; }
