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

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("paymentapi listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
