// Command paymentapi starts the in-memory payment API described in docs/SPEC.md.
//
//	go run .            # listens on :8080 (or $PORT)
//	GET /healthz -> 200 {"status":"ok"}
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"paymentapi/handlers"
	"paymentapi/store"
)

func main() {
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

	log.Printf("paymentapi listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
