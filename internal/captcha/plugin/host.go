package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// Client launches a solver plugin subprocess and speaks the stdio-JSON
// protocol. It implements captcha.Solver, so a plugin is used exactly like an
// in-process solver. The plugin only ever receives a Challenge — never the page.
type Client struct {
	path string
	args []string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	name   string
	closed bool

	// transport, when set, replaces the subprocess (tests run Serve over pipes).
	transport func() (io.WriteCloser, io.Reader, error)
}

// NewClient prepares a plugin client for the binary at path.
func NewClient(path string, args ...string) *Client {
	return &Client{path: path, args: args, name: "plugin:" + baseName(path)}
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Name implements captcha.Solver.
func (c *Client) Name() string { return c.name }

func (c *Client) ensure() error {
	if c.stdin != nil {
		return nil
	}
	if c.transport != nil {
		w, r, err := c.transport()
		if err != nil {
			return err
		}
		c.stdin = w
		c.stdout = bufio.NewReader(r)
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return fmt.Errorf("plugin: handshake read: %w", err)
		}
		if strings.TrimSpace(line) != Handshake {
			return fmt.Errorf("plugin: bad handshake %q", strings.TrimSpace(line))
		}
		return nil
	}
	cmd := exec.Command(c.path, c.args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("plugin: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("plugin: stdout: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin: start %s: %w", c.path, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReader(stdout)
	// Read the handshake line.
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return fmt.Errorf("plugin: handshake read: %w", err)
	}
	if strings.TrimSpace(line) != Handshake {
		return fmt.Errorf("plugin: bad handshake %q", strings.TrimSpace(line))
	}
	return nil
}

func (c *Client) rpc(req Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Response{}, errors.New("plugin: closed")
	}
	if err := c.ensure(); err != nil {
		return Response{}, err
	}
	data, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return Response{}, fmt.Errorf("plugin: write: %w", err)
	}
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return Response{}, fmt.Errorf("plugin: read: %w", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return Response{}, fmt.Errorf("plugin: decode: %w", err)
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// Describe implements captcha.Solver (called once at load).
func (c *Client) Describe() captcha.Capabilities {
	resp, err := c.rpc(Request{Method: MethodDescribe})
	if err != nil || resp.Caps == nil {
		return captcha.Capabilities{}
	}
	return *resp.Caps
}

// Solve implements captcha.Solver.
func (c *Client) Solve(ctx context.Context, ch captcha.Challenge) (captcha.Result, error) {
	type out struct {
		r   captcha.Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		resp, err := c.rpc(Request{Method: MethodSolve, Challenge: &ch, SolveID: ch.Params["solve_id"]})
		if err != nil {
			done <- out{err: err}
			return
		}
		if resp.Result == nil {
			done <- out{err: captcha.ErrDeclined}
			return
		}
		done <- out{r: *resp.Result}
	}()
	select {
	case o := <-done:
		return o.r, o.err
	case <-ctx.Done():
		return captcha.Result{}, ctx.Err()
	}
}

// Report implements captcha.Solver.
func (c *Client) Report(_ context.Context, solveID string, accepted bool) error {
	_, err := c.rpc(Request{Method: MethodReport, SolveID: solveID, Accepted: accepted})
	return err
}

// Close terminates the plugin subprocess.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil {
		done := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
		}
	}
	return nil
}
