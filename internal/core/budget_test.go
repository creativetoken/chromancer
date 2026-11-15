package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBudgetCheckAndSpend(t *testing.T) {
	b := NewBudget(Limits{Tokens: 100, Solves: 2})
	if err := b.Check(ResTokens, 50); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	b.Spend(ResTokens, 60)
	if err := b.Check(ResTokens, 50); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("want exhausted, got %v", err)
	}
	// unlimited resource
	if err := b.Check(ResCostUSD, 1e9); err != nil {
		t.Fatalf("unlimited should pass: %v", err)
	}
	st := b.Snapshot()
	if st.Spent[ResTokens] != 60 || st.Limits[ResSolves] != 2 {
		t.Fatalf("bad snapshot %+v", st)
	}
}

func TestBudgetContextCancelsOnOverspend(t *testing.T) {
	b := NewBudget(Limits{Steps: 1})
	ctx, cancel := b.Context(context.Background())
	defer cancel()
	b.Spend(ResSteps, 2)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context not cancelled on overspend")
	}
}

func TestBudgetTimeLimit(t *testing.T) {
	b := NewBudget(Limits{Time: 10 * time.Millisecond})
	ctx, cancel := b.Context(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("time budget did not cancel")
	}
	time.Sleep(15 * time.Millisecond)
	if err := b.Check(ResTime, 0); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("want time exhausted, got %v", err)
	}
}
