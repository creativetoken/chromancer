package camoufox

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed launch_server.py
var launchServerPy []byte

// SidecarOptions configures a Camoufox Playwright-server sidecar. Secrets (proxy
// credentials) are passed via the environment, never argv, so they never appear
// in the process list.
type SidecarOptions struct {
	Python          string // python interpreter (default "python3")
	ProxyURL        string
	GeoIP           bool
	FingerprintPath string // persisted per-account fingerprint/config file
	Region          string
	Logger          func(format string, args ...any)
}

// ready is the JSON line the sidecar prints once the Playwright server is up.
type ready struct {
	WS                string `json:"ws"`
	Display           string `json:"display"`
	PlaywrightVersion string `json:"playwright_version"`
	CamoufoxVersion   string `json:"camoufox_version"`
	Error             string `json:"error,omitempty"`
}

// Sidecar is a running Camoufox Playwright server for one account.
type Sidecar struct {
	WS                string
	Display           string
	PlaywrightVersion string
	CamoufoxVersion   string

	cmd     *exec.Cmd
	script  string // temp path of the embedded python script
	stopped sync.Once
	logger  func(format string, args ...any)
}

// parseReady scans the sidecar's stdout for the JSON ready line. It tolerates
// non-JSON log noise before the ready line and returns the first JSON object it
// can decode (which carries either the ws endpoint or an error).
func parseReady(r io.Reader) (ready, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var rd ready
		if err := json.Unmarshal([]byte(line), &rd); err != nil {
			continue
		}
		if rd.Error != "" {
			return rd, fmt.Errorf("camoufox sidecar: %s", rd.Error)
		}
		if rd.WS == "" {
			return rd, fmt.Errorf("camoufox sidecar: ready line has no ws endpoint")
		}
		return rd, nil
	}
	if err := sc.Err(); err != nil {
		return ready{}, fmt.Errorf("camoufox sidecar: read stdout: %w", err)
	}
	return ready{}, fmt.Errorf("camoufox sidecar: exited before printing a ready line")
}

// StartSidecar writes the embedded launcher to a temp file and starts it,
// waiting (up to the ctx deadline) for the ready line. The process is put in its
// own process group so Close can kill the whole tree (Xvfb + Firefox) with no
// orphans. The Playwright version it reports is checked against the pinned
// PlaywrightVersion.
func StartSidecar(ctx context.Context, o SidecarOptions) (*Sidecar, error) {
	py := o.Python
	if py == "" {
		py = "python3"
	}
	f, err := os.CreateTemp("", "camoufox-launch-*.py")
	if err != nil {
		return nil, fmt.Errorf("camoufox: temp script: %w", err)
	}
	if _, err := f.Write(launchServerPy); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("camoufox: write script: %w", err)
	}
	f.Close()

	cmd := exec.CommandContext(ctx, py, "-I", f.Name())
	cmd.Env = append(os.Environ(),
		"CAMOUFOX_HOST=127.0.0.1",
		"CAMOUFOX_PORT=0",
		"CAMOUFOX_PROXY="+o.ProxyURL,
		"CAMOUFOX_FINGERPRINT="+o.FingerprintPath,
		"CAMOUFOX_REGION="+o.Region,
	)
	if o.GeoIP {
		cmd.Env = append(cmd.Env, "CAMOUFOX_GEOIP=1")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group for a clean group-kill

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.Remove(f.Name())
		return nil, fmt.Errorf("camoufox: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Remove(f.Name())
		return nil, fmt.Errorf("camoufox: start %s: %w", py, err)
	}

	s := &Sidecar{cmd: cmd, script: f.Name(), logger: o.Logger}

	// Parse the ready line with the ctx deadline honoured.
	type res struct {
		rd  ready
		err error
	}
	ch := make(chan res, 1)
	go func() { rd, perr := parseReady(stdout); ch <- res{rd, perr} }()

	select {
	case <-ctx.Done():
		s.Close()
		return nil, fmt.Errorf("camoufox: sidecar did not become ready: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			s.Close()
			return nil, r.err
		}
		s.WS, s.Display, s.PlaywrightVersion, s.CamoufoxVersion = r.rd.WS, r.rd.Display, r.rd.PlaywrightVersion, r.rd.CamoufoxVersion
	}

	if err := CheckPlaywrightVersion(s.PlaywrightVersion); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Sidecar) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger(format, args...)
	}
}

// Close kills the sidecar's whole process group (Xvfb + Firefox + Playwright
// server) and removes the temp script. Safe to call more than once.
func (s *Sidecar) Close() {
	s.stopped.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			pid := s.cmd.Process.Pid
			// Negative pid → signal the whole process group.
			if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
				_ = s.cmd.Process.Kill()
			} else {
				// Give it a moment, then SIGKILL the group if still alive.
				done := make(chan struct{})
				go func() { _, _ = s.cmd.Process.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}
		if s.script != "" {
			_ = os.Remove(s.script)
		}
		s.logf("camoufox sidecar stopped")
	})
}
