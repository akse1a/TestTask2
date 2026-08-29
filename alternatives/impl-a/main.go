// Command paymentapi implements the payment mini-API described in docs/SPEC.md.
//
// Design lane (implementer A): a single global sync.Mutex guards an in-memory
// map store. Idempotency is handled with the simplest possible check-then-insert
// performed entirely under that lock, so correctness is easy to see. Clarity is
// favored over cleverness.
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	srv := NewServer()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	log.Printf("payment api listening on %s", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
