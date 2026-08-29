// Package store is the in-memory, thread-safe persistence layer for payments.
//
// Concurrency model (SPEC §5):
//   - A single sync.RWMutex guards the payments map. Reads (GET) take the read
//     lock; mutations (create/cancel) take the write lock.
//   - Idempotency keys are reserved through a per-key mutex. Concurrent creators
//     using the same Idempotency-Key contend on that one key's lock, so exactly
//     one of them creates the payment and every other caller observes the
//     already-created result. This is the cooperation point the "breaker" agent
//     probes: two simultaneous identical POSTs create exactly one payment.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"paymentapi/apperr"
)

// Payment statuses (SPEC §3).
const (
	StatusCreated  = "created"
	StatusCanceled = "canceled"
)

// Payment is the domain object serialized to clients (SPEC §1).
type Payment struct {
	ID             string `json:"id"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// fingerprint is the normalized request body used to compare repeated calls
// under the same idempotency key. Comparison is on the (amount, currency) pair,
// not the raw bytes, so field order and whitespace never matter (SPEC §2.1).
type fingerprint struct {
	amount   int64
	currency string
}

// idemEntry is the reservation slot for one idempotency key. Its mutex
// serializes creators for that key; once done is true, pay/fp are immutable.
type idemEntry struct {
	mu   sync.Mutex
	done bool
	fp   fingerprint
	pay  Payment
}

// Store holds all payments in memory.
type Store struct {
	mu       sync.RWMutex // guards payments
	payments map[string]*Payment

	keyMu sync.Mutex // guards keys map (short critical section only)
	keys  map[string]*idemEntry
}

// New returns an empty, ready-to-use store.
func New() *Store {
	return &Store{
		payments: make(map[string]*Payment),
		keys:     make(map[string]*idemEntry),
	}
}

// getOrCreateEntry atomically returns the reservation slot for key, creating it
// on first use. The keyMu critical section is tiny (map lookup/insert only); the
// real work happens under the returned entry's own mutex.
func (s *Store) getOrCreateEntry(key string) *idemEntry {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	e, ok := s.keys[key]
	if !ok {
		e = &idemEntry{}
		s.keys[key] = e
	}
	return e
}

// CreatePayment creates a payment for the given idempotency key, or returns the
// previously created one. The boolean reports whether a new payment was created
// (true → HTTP 201, false → HTTP 200). A reuse of the key with a different body
// yields apperr.ErrIdempotencyKeyReuse (HTTP 409) and creates nothing.
func (s *Store) CreatePayment(key string, amount int64, currency string) (Payment, bool, error) {
	entry := s.getOrCreateEntry(key)

	// Serialize all creators sharing this key. Exactly one proceeds past the
	// !done branch; the rest replay the stored result or report reuse.
	entry.mu.Lock()
	defer entry.mu.Unlock()

	fp := fingerprint{amount: amount, currency: currency}

	if entry.done {
		if entry.fp != fp {
			return Payment{}, false, apperr.ErrIdempotencyKeyReuse
		}
		return entry.pay, false, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	p := &Payment{
		ID:             newID(),
		AmountMinor:    amount,
		Currency:       currency,
		Status:         StatusCreated,
		IdempotencyKey: key,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	s.mu.Lock()
	s.payments[p.ID] = p
	s.mu.Unlock()

	entry.pay = *p
	entry.fp = fp
	entry.done = true

	return *p, true, nil
}

// GetPayment returns a copy of the payment with the given id, or
// apperr.ErrPaymentNotFound.
func (s *Store) GetPayment(id string) (Payment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.payments[id]
	if !ok {
		return Payment{}, apperr.ErrPaymentNotFound
	}
	return *p, nil
}

// CancelPayment cancels the payment (SPEC §2.3). A created payment transitions
// to canceled with a fresh updated_at. An already-canceled payment is returned
// unchanged (idempotent cancel). A missing payment yields
// apperr.ErrPaymentNotFound.
func (s *Store) CancelPayment(id string) (Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return Payment{}, apperr.ErrPaymentNotFound
	}
	if p.Status == StatusCreated {
		p.Status = StatusCanceled
		p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return *p, nil
}

// newID returns a unique, unpredictable payment id like "pay_<hex>".
func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is effectively impossible on supported platforms;
		// fall back to a timestamp so we never emit an empty id.
		return "pay_" + hex.EncodeToString([]byte(time.Now().UTC().String()))
	}
	return "pay_" + hex.EncodeToString(b)
}
