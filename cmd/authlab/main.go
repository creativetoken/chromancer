// Command authlab serves the offline mock-app lab used for e2e tests and demos.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/creativetoken/chromancer/internal/lab"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8089", "listen address")
	flag.Parse()

	srv := lab.New()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	base := "http://" + ln.Addr().String()
	srv.SetBase(base)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
	}()
	fmt.Printf("authlab listening on %s\n", base)
	for _, a := range srv.Apps() {
		fmt.Printf("  %s/%s/\n", base, a)
	}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
