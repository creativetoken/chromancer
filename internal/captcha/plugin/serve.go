package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// Serve runs a captcha.Solver as a stdio plugin: it prints the handshake, then
// reads newline-JSON requests and writes responses until stdin closes. A plugin
// binary's main() calls plugin.Serve(mySolver).
func Serve(s captcha.Solver, in io.Reader, out io.Writer) error {
	w := bufio.NewWriter(out)
	fmt.Fprintln(w, Handshake)
	_ = w.Flush()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<16), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeResp(w, Response{Error: "bad request: " + err.Error()})
			continue
		}
		writeResp(w, handle(s, req))
	}
	return sc.Err()
}

func handle(s captcha.Solver, req Request) Response {
	switch req.Method {
	case MethodDescribe:
		caps := s.Describe()
		return Response{Caps: &caps}
	case MethodSolve:
		if req.Challenge == nil {
			return Response{Error: "solve: missing challenge"}
		}
		r, err := s.Solve(context.Background(), *req.Challenge)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if r.SolveID == "" {
			r.SolveID = req.SolveID
		}
		return Response{Result: &r}
	case MethodReport:
		if err := s.Report(context.Background(), req.SolveID, req.Accepted); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	default:
		return Response{Error: "unknown method: " + req.Method}
	}
}

func writeResp(w *bufio.Writer, resp Response) {
	data, _ := json.Marshal(resp)
	_, _ = w.Write(append(data, '\n'))
	_ = w.Flush()
}
