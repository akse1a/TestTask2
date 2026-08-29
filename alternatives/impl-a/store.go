package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Store is the in-memory, thread-safe payment store.
//
// Design lane (implementer A): a single global sync.Mutex guards every access
// to both maps. This is intentionally the simplest correct design — there is
// exactly one lock, and every method takes it for its whole body, so there is
// no chance of a partially-updated view or a lost race.
type Store struct {
	mu    sync.Mutex
	byID  map[string]*Payment // id -> payment
	byKey map[string]string   // idempotency key -> payment id
	now   func() time.Time    // injectable clock (tests)
	newID func() string       // injectable id generator (tests)
}

// NewStore returns an empty store with real clock and id generator.
func NewStore() *Store {
	return &Store{
		byID:  make(map[string]*Payment),
		byKey: make(map[string]string),
		now:   func() time.Time { return time.Now().UTC() },
		newID: generateID,
	}
}

// generateID returns a random, unique-enough id like "pay_<hex>".
func generateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should not fail; fall back to a timestamp-based value so
		// the server never panics on id generation.
		return "pay_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "pay_" + hex.EncodeToString(b[:])
}

// createResult reports the outcome of CreateIdempotent.
type createResult int

const (
	createdNew      createResult = iota // brand new payment (respond 201)
	createdExisting                     // idempotent replay, same body (respond 200)
	createdConflict                     // same key, different body (respond 409)
)

// CreateIdempotent performs the whole create-or-replay decision under a single
// lock (SPEC §2.1, §5). The returned *Payment is a copy safe to serialize
// without holding the lock.
func (s *Store) CreateIdempotent(key string, amountMinor int64, currency string) (Payment, createResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If we already have a payment for this key, this is either an idempotent
	// replay (same body) or a conflict (different body). Either way we create
	// nothing new.
	if id, ok := s.byKey[key]; ok {
		existing := s.byID[id]
		if existing.AmountMinor == amountMinor && existing.Currency == currency {
			return *existing, createdExisting
		}
		return Payment{}, createdConflict
	}

	// Brand new payment.
	now := s.now().Format(time.RFC3339)
	p := &Payment{
		ID:             s.newID(),
		AmountMinor:    amountMinor,
		Currency:       currency,
		Status:         StatusCreated,
		IdempotencyKey: key,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.byID[p.ID] = p
	s.byKey[key] = p.ID
	return *p, createdNew
}

// Get returns a copy of the payment with the given id.
func (s *Store) Get(id string) (Payment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return Payment{}, false
	}
	return *p, true
}

// Cancel transitions a payment to canceled (SPEC §2.3). Canceling an already
// canceled payment is idempotent and not an error. Returns (payment, found).
func (s *Store) Cancel(id string) (Payment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return Payment{}, false
	}
	if p.Status == StatusCreated {
		p.Status = StatusCanceled
		p.UpdatedAt = s.now().Format(time.RFC3339)
	}
	// Already canceled: return the same object unchanged (idempotent).
	return *p, true
}

// Count returns the number of stored payments (used by tests).
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}
