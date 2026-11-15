package plugin

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/solver"
)

// inProcClient wires a Client to a Serve goroutine over pipes (no subprocess).
func inProcClient(t *testing.T, s captcha.Solver) *Client {
	t.Helper()
	pluginIn, hostOut := io.Pipe() // host writes hostOut → plugin reads pluginIn
	hostIn, pluginOut := io.Pipe() // plugin writes pluginOut → host reads hostIn
	go func() { _ = Serve(s, pluginIn, pluginOut) }()
	c := &Client{name: "plugin:inproc"}
	c.transport = func() (io.WriteCloser, io.Reader, error) {
		return hostOut, hostIn, nil
	}
	t.Cleanup(func() { _ = c.Close(); _ = pluginIn.Close(); _ = pluginOut.Close() })
	return c
}

func TestServeProtocolInProcess(t *testing.T) {
	ts := solver.NewTestMode(map[string]string{"": "lab-ok"})
	c := inProcClient(t, ts)
	caps := c.Describe()
	if len(caps.Vendors) == 0 || caps.Vendors[0] != captcha.VendorMock {
		t.Fatalf("describe over protocol: %+v", caps)
	}
	res, err := c.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorMock, SiteKey: "x", Params: map[string]string{"solve_id": "s1"}})
	if err != nil || res.Token != "lab-ok" {
		t.Fatalf("solve over protocol: %+v %v", res, err)
	}
	if res.SolveID != "s1" {
		t.Fatalf("solve id not echoed: %q", res.SolveID)
	}
	if err := c.Report(context.Background(), "s1", true); err != nil {
		t.Fatal(err)
	}
}

// TestConformanceInProcess runs the conformance suite over the protocol.
func TestConformanceInProcess(t *testing.T) {
	Conform(t, func() captcha.Solver {
		return inProcClient(t, solver.NewTestMode(map[string]string{"": "lab-ok"}))
	})
}

// TestSubprocessMockSolver builds the real mocksolver binary and drives it as a
// subprocess plugin, then runs conformance against it.
func TestSubprocessMockSolver(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "mocksolver")
	build := exec.Command("go", "build", "-o", bin, "github.com/creativetoken/chromancer/cmd/mocksolver")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mocksolver: %v\n%s", err, out)
	}
	t.Setenv("MOCKSOLVER_TOKEN", "lab-captcha-ok")

	c := NewClient(bin)
	defer c.Close()
	res, err := c.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorMock, SiteKey: "k", Params: map[string]string{"solve_id": "z1"}})
	if err != nil || res.Token != "lab-captcha-ok" {
		t.Fatalf("subprocess solve: %+v %v", res, err)
	}

	Conform(t, func() captcha.Solver {
		cl := NewClient(bin)
		t.Cleanup(func() { cl.Close() })
		return cl
	})
}
