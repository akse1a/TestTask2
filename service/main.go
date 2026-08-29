// Command paymentapi starts the in-memory payment API described in docs/SPEC.md.
//
//	go run .            # listens on :8080 (or $PORT)
//	GET /healthz -> 200 {"status":"ok"}
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"paymentapi/handlers"
	"paymentapi/store"
)

const shutdownTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatalf("paymentapi: %v", err)
	}
}

// run wires the server and blocks until a termination signal, then drains
// in-flight requests within shutdownTimeout. It returns an error instead of
// calling log.Fatal itself so the startup/shutdown path stays testable and has a
// single exit point.
func run() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	api := handlers.New(store.New())

	// Timeouts bound how long a single connection may tie up a goroutine, so a
	// slow or stalled client (Slowloris-style, on headers or body) cannot hold
	// resources indefinitely. ReadHeaderTimeout guards the header phase;
	// Read/Write/Idle bound the body, response, and keep-alive phases.
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Cancel the context on SIGINT/SIGTERM to trigger a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("paymentapi listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Println("paymentapi: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
