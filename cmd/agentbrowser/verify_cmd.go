package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/verify"
)

// resourcesPath returns the resources YAML path from the flag or
// AGENTBROWSER_RESOURCES.
func resourcesPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("AGENTBROWSER_RESOURCES")
}

// cmdMailbox handles `mailbox login --mailbox ID`.
func cmdMailbox(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "login" {
		return fmt.Errorf("usage: agentbrowser mailbox login --mailbox ID [--resources FILE]")
	}
	fs := flag.NewFlagSet("mailbox login", flag.ContinueOnError)
	id := fs.String("mailbox", "", "mailbox id from the resources config")
	resPath := fs.String("resources", "", "resources YAML (default $AGENTBROWSER_RESOURCES)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--mailbox is required")
	}
	res, err := verify.LoadResources(resourcesPath(*resPath))
	if err != nil {
		return err
	}
	var mb *verify.Mailbox
	for i := range res.Mailboxes {
		if res.Mailboxes[i].ID == *id {
			mb = &res.Mailboxes[i]
		}
	}
	if mb == nil {
		return fmt.Errorf("no mailbox %q in the resources config", *id)
	}
	a, err := app.New(config.Default())
	if err != nil {
		return err
	}
	return a.RunMailboxLogin(ctx, *mb)
}

// cmdVerify handles `verify test --account A --channel sms|email|totp`.
func cmdVerify(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "test" {
		return fmt.Errorf("usage: agentbrowser verify test --account A --channel sms|email|totp [--target T] [--lookback D] [--quiet]")
	}
	fs := flag.NewFlagSet("verify test", flag.ContinueOnError)
	account := fs.String("account", "", "account id")
	channel := fs.String("channel", "", "sms | email | totp")
	target := fs.String("target", "", "target app/site (for TOTP per-target seeds)")
	lookback := fs.Duration("lookback", time.Hour, "how far back to accept an existing message")
	timeout := fs.Duration("timeout", 90*time.Second, "how long to wait")
	quiet := fs.Bool("quiet", false, "print only pass/fail + timing, never the value")
	resPath := fs.String("resources", "", "resources YAML (default $AGENTBROWSER_RESOURCES)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *account == "" || *channel == "" {
		return fmt.Errorf("--account and --channel are required")
	}
	ch := verify.Channel(*channel)
	res, err := verify.LoadResources(resourcesPath(*resPath))
	if err != nil {
		return err
	}
	a, err := app.New(config.Default())
	if err != nil {
		return err
	}
	chain := a.BuildVerifyChain(res, verify.NewClaimStore())
	if !chain.Supports(ch, *account) {
		fmt.Printf("verify %s/%s: UNSUPPORTED (no resource configured for this account and channel)\n", *account, ch)
		return nil
	}
	req := verify.Request{
		Channel: ch, Account: *account, Target: *target,
		WantLink: false,
		Since:    time.Now().Add(-*lookback),
		Deadline: time.Now().Add(*timeout),
	}
	cctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	start := time.Now()
	r, err := chain.GetCode(cctx, req)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		fmt.Printf("verify %s/%s: FAIL (%v) in %s\n", *account, ch, err, elapsed)
		return err
	}
	kind := "code"
	if r.Kind == verify.ResLink {
		kind = "link"
	} else if r.Kind == verify.ResApproved {
		kind = "approval"
	}
	if *quiet {
		fmt.Printf("verify %s/%s: PASS (got a %s) in %s\n", *account, ch, kind, elapsed)
		return nil
	}
	val := r.Code
	if r.Kind == verify.ResLink {
		val = r.Link
	}
	fmt.Printf("verify %s/%s: PASS (%s = %s) in %s\n", *account, ch, kind, val, elapsed)
	return nil
}
