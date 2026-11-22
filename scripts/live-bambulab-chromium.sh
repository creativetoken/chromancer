#!/usr/bin/env bash
# Run the live Bambu Lab sign-in on the CHROMIUM backend using the system Google
# Chrome, headful (a real visible window). This is the path that previously logged
# in end-to-end on bambulab, OTP step included; rod's TypeInto has the guarded
# code-field fallback. Because the window is real and on your screen, any human
# step (CAPTCHA, an odd challenge) can be handled directly — no noVNC needed.
#
# Usage (from the repo root):  bash scripts/live-bambulab-chromium.sh
#
# Restores the saved session on ANY exit (success/failure/Ctrl-C).
set -u

SESS=.workspace/accounts/bambulab/session.enc
restore() {
  if [ -f "$SESS.save" ]; then
    mv -f "$SESS.save" "$SESS" && echo "[live] restored saved Bambu Lab session"
  fi
}
trap restore EXIT INT TERM
[ -f "$SESS" ] && mv "$SESS" "$SESS.save"   # force a real sign-in

set -a; . ./.env.live; set +a
export AGENTBROWSER_BACKEND=chromium
export AGENTBROWSER_CHROME="${AGENTBROWSER_CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
export AGENTBROWSER_WORKSPACE="$PWD/.workspace"
export AGENTBROWSER_RESOURCES="${AGENTBROWSER_RESOURCES:-$PWD/configs/resources.example.yaml}"

go run ./cmd/agentbrowser auth --account bambulab --mode login --auth-driver agent --headful \
  --login-url https://bambulab.com/en-gb/sign-in
