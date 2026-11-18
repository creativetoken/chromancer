package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsStrandedClick(t *testing.T) {
	stranded := []error{
		context.DeadlineExceeded,
		fmt.Errorf("submit: %w", context.DeadlineExceeded),
		errors.New("browser: click: context deadline exceeded"),
		errors.New("browser: wait enabled: context deadline exceeded"),
		errors.New("browser: wait interactable: timeout"),
		errors.New("browser: wait stable: element is detached"),
		errors.New(`browser: element "[data-ab-handle=\"5\"]": context deadline exceeded`),
		errors.New("cannot find node with given id"),
	}
	for _, e := range stranded {
		if !isStrandedClick(e) {
			t.Errorf("expected stranded: %v", e)
		}
	}
	// A genuine, non-recoverable failure must NOT be retried as a stranded click.
	notStranded := []error{
		nil,
		errors.New("flow_failed: no submit control"),
		errors.New("account is locked"),
		errors.New("browser: navigate: connection refused"),
	}
	for _, e := range notStranded {
		if isStrandedClick(e) {
			t.Errorf("did NOT expect stranded: %v", e)
		}
	}
}
