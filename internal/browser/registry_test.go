package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeBrowser struct{ Browser }

func TestRegisterAndOpen(t *testing.T) {
	name := "test-fake-backend"
	want := &fakeBrowser{}
	Register(name, func(ctx context.Context, o Options) (Browser, error) { return want, nil })

	got, err := Open(context.Background(), name, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != want {
		t.Fatalf("Open returned a different browser")
	}

	found := false
	for _, n := range Registered() {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Errorf("Registered() does not list %q: %v", name, Registered())
	}
}

func TestOpenUnknownBackend(t *testing.T) {
	_, err := Open(context.Background(), "nope-not-registered", Options{})
	if err == nil {
		t.Fatal("expected an error for an unknown backend")
	}
	if !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("error should name the problem: %v", err)
	}
}

func TestOpenEmptyDefaultsToChromium(t *testing.T) {
	// With no chromium backend registered in this unit-test binary, Open("")
	// must still resolve the default name to "chromium" (then fail to find it).
	_, err := Open(context.Background(), "", Options{})
	if err == nil {
		t.Skip("a chromium backend is registered in this binary; default resolved")
	}
	if !strings.Contains(err.Error(), `"chromium"`) {
		t.Errorf("empty backend should default to chromium, got: %v", err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	name := "dup-backend"
	Register(name, func(ctx context.Context, o Options) (Browser, error) { return nil, errors.New("x") })
	defer func() {
		if recover() == nil {
			t.Error("expected a panic on duplicate Register")
		}
	}()
	Register(name, func(ctx context.Context, o Options) (Browser, error) { return nil, errors.New("y") })
}
