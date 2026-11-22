#!/usr/bin/env bash
# Run a fresh live Bambu Lab sign-in through the agent on the camoufox backend.
#
# The saved session is moved aside to force a REAL sign-in, and restored on ANY
# exit (success, failure, or Ctrl-C) via the trap below — so the account's saved
# session is always put back. The agentbrowser run prints a one-line summary
# (result / driver / backend / mode / steps / screenshots / tokens) at the end.
#
# Usage (from the repo root):  bash scripts/live-bambulab.sh
#
# Requires: the agentbrowser-camoufox-test:latest image, and a git-ignored
# .env.live with ANTHROPIC_API_KEY, GMAIL_* and the account env (see docs).
set -u

SESS=.workspace/accounts/bambulab/session.enc
restore() {
  if [ -f "$SESS.save" ]; then
    mv -f "$SESS.save" "$SESS" && echo "[live] restored saved Bambu Lab session"
  fi
}
trap restore EXIT INT TERM

# Move the saved session aside so the run has to sign in fresh.
[ -f "$SESS" ] && mv "$SESS" "$SESS.save"

# The human-handoff noVNC view runs INSIDE the container; publish a fixed port to
# the host (-p) and bind/advertise it so its URL is openable from macOS. If a
# login needs a human (CAPTCHA / unusual challenge), open the printed
# http://127.0.0.1:${HANDOFF_PORT}/vnc.html?... link, solve it, then press Enter.
HANDOFF_PORT="${HANDOFF_PORT:-8899}"
docker run --rm -it -p "127.0.0.1:${HANDOFF_PORT}:${HANDOFF_PORT}" -v "$PWD":/src -w /src agentbrowser-camoufox-test:latest bash -c \
 'set -a; . /src/.env.live; set +a; export AGENTBROWSER_BACKEND=camoufox AGENTBROWSER_WORKSPACE=/src/.workspace AGENTBROWSER_RESOURCES=/src/configs/resources.example.yaml PATH=/usr/local/go/bin:$PATH; \
  export AGENTBROWSER_HANDOFF_BIND=0.0.0.0:'"${HANDOFF_PORT}"' AGENTBROWSER_HANDOFF_ADVERTISE=127.0.0.1; \
  Xvfb :99 -screen 0 1280x900x24 >/dev/null 2>&1 & export DISPLAY=:99; sleep 1; \
  go run ./cmd/agentbrowser auth --account bambulab --mode login --auth-driver agent --login-url https://bambulab.com/en-gb/sign-in'

# restore runs automatically via the trap.
