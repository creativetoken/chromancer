// Command agentbrowser authenticates to a web app with operator-provided
// credentials and hands back a reusable session bundle + auth recording.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/eval"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/lab"
	"github.com/creativetoken/chromancer/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "creds":
		err = cmdCreds(ctx, os.Args[2:])
	case "auth":
		err = cmdAuth(ctx, os.Args[2:])
	case "export-session":
		err = cmdExport(ctx, os.Args[2:])
	case "crawl":
		err = cmdCrawl(ctx, os.Args[2:])
	case "eval":
		err = cmdEval(ctx, os.Args[2:])
	case "mcp":
		err = cmdMCP(ctx, os.Args[2:])
	case "mailbox":
		err = cmdMailbox(ctx, os.Args[2:])
	case "verify":
		err = cmdVerify(ctx, os.Args[2:])
	case "newkey":
		k, e := identity.NewRandomKey()
		if e != nil {
			err = e
		} else {
			fmt.Println(k)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `agentbrowser — authenticate and hand back a session

Usage:
  agentbrowser creds put --target URL --account NAME --username U --password P [--totp-seed S]
  agentbrowser auth --account NAME --mode login|signup [--login-url URL] [--signup-url URL] [--headful]
  agentbrowser export-session --account NAME
  agentbrowser crawl --account NAME [--driver bfs|llm|hybrid] [--prompt "..."] [--headful]
  agentbrowser eval [--runs N]            # benchmark the configured model vs the lab
  agentbrowser mcp                        # run the MCP server over stdio
  agentbrowser newkey

Environment:
  AGENTBROWSER_KEY        base64 of 32 random bytes (credential/session encryption)
  AGENTBROWSER_WORKSPACE  workspace dir (default .workspace)
  OPENAI_API_KEY/_BASE_URL/_MODEL  enable a real LLM (else a mock is used)
`)
}

func cmdCreds(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "put" {
		return fmt.Errorf("usage: creds put --target URL --account NAME --username U --password P")
	}
	fs := flag.NewFlagSet("creds put", flag.ContinueOnError)
	target := fs.String("target", "", "app root URL")
	account := fs.String("account", "", "account name")
	username := fs.String("username", "", "username/email")
	password := fs.String("password", "", "password")
	totp := fs.String("totp-seed", "", "optional TOTP seed (base32)")
	phone := fs.String("phone", "", "optional phone")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *target == "" || *account == "" || *username == "" || *password == "" {
		return fmt.Errorf("--target, --account, --username, --password are required")
	}
	a, err := app.New(config.Default())
	if err != nil {
		return err
	}
	if err := a.Creds.PutCredentials(ctx, *target, identity.AccountID(*account), identity.Credentials{
		UsernameOrEmail: *username, Password: *password, TOTPSeed: *totp, Phone: *phone,
	}); err != nil {
		return err
	}
	fmt.Printf("stored credentials for %s (target %s)\n", *account, *target)
	return nil
}

func cmdAuth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	account := fs.String("account", "", "account name")
	mode := fs.String("mode", "login", "login|signup")
	loginURL := fs.String("login-url", "", "entrypoint hint for login")
	signupURL := fs.String("signup-url", "", "entrypoint hint for signup")
	headful := fs.Bool("headful", false, "run with a visible browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *account == "" {
		return fmt.Errorf("--account is required")
	}
	m := auth.Login
	if *mode == "signup" {
		m = auth.Signup
	} else if *mode != "login" {
		return fmt.Errorf("--mode must be login or signup")
	}
	cfg := config.Default()
	if *headful {
		cfg.Headless = false
	}
	a, err := app.New(cfg)
	if err != nil {
		return err
	}
	res := a.RunAuth(ctx, *account, m, auth.Hints{LoginURL: *loginURL, SignupURL: *signupURL}, nil)
	if res.Err != nil {
		out, _ := json.MarshalIndent(map[string]string{"status": "failed", "reason": res.Reason, "error": res.Err.Error()}, "", "  ")
		fmt.Println(string(out))
		return res.Err
	}
	out, _ := json.MarshalIndent(map[string]any{
		"status":        "done",
		"bundle_ref":    res.Result.BundleRef,
		"recording_ref": res.Result.RecordingRef,
		"trace":         res.Result.Trace,
	}, "", "  ")
	fmt.Println(string(out))
	return nil
}

func cmdCrawl(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("crawl", flag.ContinueOnError)
	account := fs.String("account", "", "account name")
	headful := fs.Bool("headful", false, "run with a visible browser")
	driver := fs.String("driver", "", "crawl driver: bfs (default) | llm | hybrid")
	prompt := fs.String("prompt", "", "operator instruction for the llm driver (implies --driver llm)")
	promptFile := fs.String("prompt-file", "", "read the operator instruction from a file")
	systemPromptFile := fs.String("system-prompt-file", "", "override the built-in system template")
	maxSteps := fs.Int("max-steps", 0, "llm driver step cap (0 = default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *account == "" {
		return fmt.Errorf("--account is required")
	}
	opts := app.CrawlOptions{Driver: *driver, Prompt: *prompt, MaxSteps: *maxSteps}
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			return fmt.Errorf("--prompt-file: %w", err)
		}
		opts.Prompt = string(b)
	}
	if *systemPromptFile != "" {
		b, err := os.ReadFile(*systemPromptFile)
		if err != nil {
			return fmt.Errorf("--system-prompt-file: %w", err)
		}
		opts.SystemPrompt = string(b)
	}
	cfg := config.Default()
	if *headful {
		cfg.Headless = false
	}
	a, err := app.New(cfg)
	if err != nil {
		return err
	}
	res := a.RunCrawl(ctx, *account, opts, nil)
	if res.Err != nil {
		out, _ := json.MarshalIndent(map[string]string{"status": "failed", "reason": res.Reason, "error": res.Err.Error()}, "", "  ")
		fmt.Println(string(out))
		return res.Err
	}
	out, _ := json.MarshalIndent(map[string]any{"status": "done", "report": res.Report}, "", "  ")
	fmt.Println(string(out))
	return nil
}

func cmdEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	runs := fs.Int("runs", 1, "runs per scenario")
	captchaMode := fs.Bool("captcha", false, "benchmark the CAPTCHA solver chain vs the mock vendor apps")
	crawlMode := fs.Bool("crawl", false, "benchmark the crawl drivers (bfs, llm, hybrid) vs the lab crawl app")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Default()
	cfg.Headless = true
	cfg.Workspace = filepath.Join(os.TempDir(), "agentbrowser-eval")
	a, err := app.New(cfg)
	if err != nil {
		return err
	}
	srv := lab.New()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	srv.SetBase(base)
	hs := &http.Server{Handler: srv.Handler()}
	go hs.Serve(ln)
	defer hs.Close()

	h := &eval.Harness{App: a, Lab: srv, LabBase: base, ModelName: cfg.LLM.Provider, Runs: *runs}
	if *crawlMode {
		rows, err := h.CrawlBench(ctx, []string{"bfs", "llm", "hybrid"})
		if err != nil {
			return err
		}
		fmt.Printf("Crawl drivers vs the lab crawl app:\n\n%s", eval.CrawlTable(rows))
		return nil
	}
	scenarios := eval.DefaultScenarios()
	if *captchaMode {
		scenarios = eval.CaptchaScenarios()
	}
	rows, err := h.Run(ctx, scenarios)
	if err != nil {
		return err
	}
	fmt.Print(eval.Table(rows))
	if *captchaMode {
		stats, err := h.CaptchaStats()
		if err != nil {
			return err
		}
		fmt.Printf("\nSolver performance (vendor × solver):\n\n%s", eval.CaptchaTable(stats))
	}
	return nil
}

func cmdMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	headful := fs.Bool("headful", false, "run browsers with a visible window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Default()
	if *headful {
		cfg.Headless = false
	}
	a, err := app.New(cfg)
	if err != nil {
		return err
	}
	jm := server.NewJobManager(a)
	srv := server.NewMCP(jm, a)
	fmt.Fprintln(os.Stderr, "agentbrowser MCP server on stdio")
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

func cmdExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export-session", flag.ContinueOnError)
	account := fs.String("account", "", "account name")
	raw := fs.Bool("bundle", false, "print the full bundle JSON instead of the reference")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *account == "" {
		return fmt.Errorf("--account is required")
	}
	a, err := app.New(config.Default())
	if err != nil {
		return err
	}
	ref, bundle, err := a.ExportSession(ctx, *account)
	if err != nil {
		return err
	}
	if *raw {
		out, _ := json.MarshalIndent(bundle, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	out, _ := json.MarshalIndent(map[string]any{"bundle_ref": ref, "replayable": bundle.Replayable, "replay_reason": bundle.ReplayReason, "origin": bundle.Origin}, "", "  ")
	fmt.Println(string(out))
	return nil
}
