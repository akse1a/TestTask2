package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := NewServer()
	addr := ":" + port
	log.Printf("payment-api listening on %s", addr)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
