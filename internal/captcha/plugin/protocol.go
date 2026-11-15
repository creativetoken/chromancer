// Package plugin hosts out-of-process CAPTCHA solver plugins. The reference
// transport is a subprocess speaking newline-delimited JSON whose message
// shapes mirror proto/solver.proto exactly; the Solver contract is identical to
// an in-process solver, so a gRPC transport could replace this behind it.
package plugin

import "github.com/creativetoken/chromancer/internal/captcha"

// Handshake is printed by a plugin on its first stdout line so the host can
// confirm it is speaking the expected protocol (go-plugin-style).
const Handshake = "AGENTBROWSER-CAPTCHA-SOLVER-1"

// method names.
const (
	MethodDescribe = "describe"
	MethodSolve    = "solve"
	MethodReport   = "report"
)

// Request is a host→plugin message.
type Request struct {
	Method    string             `json:"method"`
	Challenge *captcha.Challenge `json:"challenge,omitempty"`
	SolveID   string             `json:"solve_id,omitempty"`
	Accepted  bool               `json:"accepted,omitempty"`
}

// Response is a plugin→host message. Exactly one of the payloads is set per the
// request method; Error is set on failure.
type Response struct {
	Caps   *captcha.Capabilities `json:"caps,omitempty"`
	Result *captcha.Result       `json:"result,omitempty"`
	Error  string                `json:"error,omitempty"`
}
