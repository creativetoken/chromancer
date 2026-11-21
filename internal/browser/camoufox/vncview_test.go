package camoufox

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestVNCViewTokenGuardLive brings up a real Xvfb + x11vnc + websockify and
// asserts the token-guarded proxy serves noVNC WITH the token and refuses every
// request WITHOUT it, including the WebSocket upgrade. Gated: needs Linux + the
// VNC toolchain + AGENTBROWSER_VNC_TEST=1 (set in the camoufox container); skips
// otherwise.
func TestVNCViewTokenGuardLive(t *testing.T) {
	if os.Getenv("AGENTBROWSER_VNC_TEST") != "1" {
		t.Skip("set AGENTBROWSER_VNC_TEST=1 (and run on Linux with Xvfb/x11vnc/websockify) to run the live VNC view test")
	}
	for _, bin := range []string{"Xvfb", "x11vnc", "websockify"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	noVNC := "/usr/share/novnc"
	if _, err := os.Stat(noVNC + "/vnc.html"); err != nil {
		t.Skipf("noVNC web root not found at %s", noVNC)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// A throwaway Xvfb display for x11vnc to serve.
	display := ":97"
	xvfb := exec.Command("Xvfb", display, "-screen", "0", "1024x768x24", "-nolisten", "tcp")
	if err := xvfb.Start(); err != nil {
		t.Fatalf("start Xvfb: %v", err)
	}
	defer func() { _ = xvfb.Process.Kill() }()
	time.Sleep(700 * time.Millisecond)

	good := "secret-handoff-token"
	view := &VNCView{Display: display, NoVNCDir: noVNC, Valid: func(tok string) bool { return tok == good }, Logger: t.Logf}
	base, err := view.Start(ctx)
	if err != nil {
		t.Fatalf("VNCView.Start: %v", err)
	}
	defer view.Close()

	// noVNC page: served with the token, refused without.
	if code := httpStatus(t, base+"/vnc.html?token="+good); code != http.StatusOK {
		t.Errorf("GET /vnc.html with token: status %d, want 200", code)
	}
	if code := httpStatus(t, base+"/vnc.html"); code != http.StatusUnauthorized {
		t.Errorf("GET /vnc.html without token: status %d, want 401", code)
	}

	// WebSocket upgrade: refused without the token, accepted (not 401) with it.
	if code := wsUpgradeStatus(t, base, "/websockify"); code != 401 {
		t.Errorf("ws upgrade without token: status %d, want 401", code)
	}
	if code := wsUpgradeStatus(t, base, "/websockify?token="+good); code == 401 {
		t.Errorf("ws upgrade WITH token was refused (401); the token must pass the gate")
	} else {
		t.Logf("ws upgrade with token → %d (not 401, as required)", code)
	}

	// The human view URL carries the token on both the page and the ws path.
	u := view.ViewURL(base, good)
	if !strings.Contains(u, "token="+good) || !strings.Contains(u, "websockify") {
		t.Errorf("ViewURL missing token/ws path: %s", u)
	}
}

func httpStatus(t *testing.T, url string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// wsUpgradeStatus sends a raw WebSocket upgrade request and returns the HTTP
// status code from the status line (101/400/401/…), without completing a real
// handshake — enough to prove the token gate.
func wsUpgradeStatus(t *testing.T, base, path string) int {
	t.Helper()
	host := strings.TrimPrefix(base, "http://")
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", host, err)
	}
	defer conn.Close()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", path, host)
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	// "HTTP/1.1 401 Unauthorized\r\n"
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		t.Fatalf("bad status line: %q", line)
	}
	code, _ := strconv.Atoi(parts[1])
	return code
}
