package camoufox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// VNCView exposes one account's virtual display (Xvfb) to a human for a handoff,
// read/write, through x11vnc + noVNC/websockify, behind the token-guarded
// ViewProxy. x11vnc binds loopback only and uses a random VNC password; the only
// reachable surface is the ViewProxy, which requires the one-time handoff token
// on every request including the WebSocket upgrade.
type VNCView struct {
	Display  string                  // e.g. ":99" (the sidecar's Xvfb display)
	Valid    func(token string) bool // handoff.ValidToken
	NoVNCDir string                  // noVNC web root (default /usr/share/novnc)
	Logger   func(format string, args ...any)

	vncCmd   *exec.Cmd
	wsCmd    *exec.Cmd
	srv      *http.Server
	authFile string
	password string
	stopped  sync.Once
}

func (v *VNCView) logf(format string, args ...any) {
	if v.Logger != nil {
		v.Logger(format, args...)
	}
}

// Start brings up x11vnc + websockify + the ViewProxy and returns the loopback
// base URL of the proxy. The human's view URL (built by the handoff) is
// <base>/vnc.html?... with the token; noVNC opens its WebSocket back through the
// same proxy carrying the token, so both the page and the socket are guarded.
func (v *VNCView) Start(ctx context.Context) (base string, err error) {
	noVNC := v.NoVNCDir
	if noVNC == "" {
		noVNC = "/usr/share/novnc"
	}
	vncPort, err := freePort()
	if err != nil {
		return "", err
	}
	wsPort, err := freePort()
	if err != nil {
		return "", err
	}

	// Random VNC password, stored obfuscated for x11vnc -rfbauth.
	v.password, err = randomHex(12)
	if err != nil {
		return "", err
	}
	af, err := os.CreateTemp("", "camoufox-vnc-*.pw")
	if err != nil {
		return "", err
	}
	af.Close()
	v.authFile = af.Name()
	if out, err := exec.CommandContext(ctx, "x11vnc", "-storepasswd", v.password, v.authFile).CombinedOutput(); err != nil {
		v.cleanupFiles()
		return "", fmt.Errorf("camoufox: x11vnc storepasswd: %w (%s)", err, out)
	}

	// x11vnc on the account's display, loopback only, auth required.
	v.vncCmd = exec.Command("x11vnc",
		"-display", v.Display, "-localhost", "-rfbport", strconv.Itoa(vncPort),
		"-rfbauth", v.authFile, "-forever", "-shared", "-noxdamage", "-quiet", "-bg", "-o", "/dev/null")
	v.vncCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if out, err := v.vncCmd.CombinedOutput(); err != nil {
		v.cleanupFiles()
		return "", fmt.Errorf("camoufox: x11vnc start: %w (%s)", err, out)
	}

	// websockify serves the noVNC web root and bridges ws → the VNC port.
	v.wsCmd = exec.Command("websockify", "--web", noVNC, "127.0.0.1:"+strconv.Itoa(wsPort), "127.0.0.1:"+strconv.Itoa(vncPort))
	v.wsCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := v.wsCmd.Start(); err != nil {
		v.Close()
		return "", fmt.Errorf("camoufox: websockify start: %w", err)
	}
	if err := waitPort(ctx, wsPort, 10*time.Second); err != nil {
		v.Close()
		return "", fmt.Errorf("camoufox: websockify not ready: %w", err)
	}

	// The token-guarded reverse proxy, bound to loopback by default. The bind
	// address may be overridden (e.g. 0.0.0.0:8899) via AGENTBROWSER_HANDOFF_BIND
	// so the view is reachable when agentbrowser runs inside a container; the
	// proxy stays token-gated regardless of bind. The host advertised in the
	// returned base URL can be set with AGENTBROWSER_HANDOFF_ADVERTISE (useful
	// when binding 0.0.0.0, which is not directly openable).
	upstream, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(wsPort))
	bindAddr := "127.0.0.1:0"
	if b := strings.TrimSpace(os.Getenv("AGENTBROWSER_HANDOFF_BIND")); b != "" {
		bindAddr = b
	}
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		v.Close()
		return "", err
	}
	v.srv = &http.Server{Handler: NewViewProxy(upstream, v.Valid)}
	go func() { _ = v.srv.Serve(ln) }()

	base = "http://" + ln.Addr().String()
	if adv := strings.TrimSpace(os.Getenv("AGENTBROWSER_HANDOFF_ADVERTISE")); adv != "" {
		if _, port, perr := net.SplitHostPort(ln.Addr().String()); perr == nil {
			base = "http://" + net.JoinHostPort(adv, port)
		}
	}
	v.logf("camoufox handoff view up at %s (display %s)", base, v.Display)
	return base, nil
}

// ViewURL builds the full human-facing noVNC URL for a token: the noVNC page,
// its WebSocket pointed back through the proxy carrying the same token, and the
// random VNC password auto-filled (safe: the surface is loopback + token-gated).
func (v *VNCView) ViewURL(base, token string) string {
	wsPath := "websockify?token=" + url.QueryEscape(token)
	q := url.Values{
		"path":        {wsPath},
		"token":       {token},
		"password":    {v.password},
		"autoconnect": {"true"},
		"resize":      {"scale"},
	}
	return base + "/vnc.html?" + q.Encode()
}

// Close tears down the proxy, websockify and x11vnc (process-group kills) and
// removes the auth file. Safe to call more than once.
func (v *VNCView) Close() {
	v.stopped.Do(func() {
		if v.srv != nil {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = v.srv.Shutdown(sctx)
			cancel()
		}
		killGroup(v.wsCmd)
		// x11vnc was started with -bg, so it daemonised; kill via the port? It
		// re-parents, so rely on -forever being bounded by the display going away
		// on sidecar close. Best-effort group kill of the launcher too.
		killGroup(v.vncCmd)
		v.cleanupFiles()
		v.logf("camoufox handoff view stopped")
	})
}

func (v *VNCView) cleanupFiles() {
	if v.authFile != "" {
		_ = os.Remove(v.authFile)
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func waitPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 250*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("port %d not listening within %s", port, timeout)
}
