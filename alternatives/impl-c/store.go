package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

// idemRecord is what we publish under an Idempotency-Key. It is immutable once
// created, so it is safe to hand out concurrently without locking.
type idemRecord struct {
	payment  *Payment
	bodyHash string // hash of the normalized (amount_minor, currency) pair
}

// Store is an in-memory, thread-safe payment store.
//
// Two sync.Maps carry the load:
//   - payments: id -> *Payment, for GET / cancel lookups.
//   - idem:     idempotency-key -> *idemRecord, for the creation hot path.
//
// The idempotency guarantee (SPEC §5) rests entirely on sync.Map's atomic
// LoadOrStore: concurrent creators each build a candidate record, and exactly
// one wins the store; every other caller receives the winner's record. No
// separate lock is taken on the creation path.
type Store struct {
	payments sync.Map // string -> *Payment
	idem     sync.Map // string -> *idemRecord
}

func NewStore() *Store { return &Store{} }

// normalizedBodyHash produces a small, order-insensitive fingerprint of the
// request body. Only the semantic pair (amount_minor, currency) matters, so
// field ordering and whitespace in the raw JSON never affect it (SPEC §2.1).
func normalizedBodyHash(amountMinor int64, currency string) string {
	canonical := strconv.FormatInt(amountMinor, 10) + "|" + currency
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// createResult reports the outcome of a create attempt.
type createResult int

const (
	createdNew       createResult = iota // brand new payment, respond 201
	createdExisting                      // same key+body, respond 200 with existing
	conflictKeyReuse                     // same key, different body, respond 409
)

// Create implements idempotent creation.
//
// It builds a candidate *Payment eagerly (id + timestamps) and attempts to
// publish it under the idempotency key with a single atomic LoadOrStore:
//   - if we win the store, the candidate is also indexed by id and returned as
//     createdNew;
//   - if a record already existed, we compare body hashes: equal -> return the
//     existing payment (createdExisting); different -> conflictKeyReuse.
//
// The candidate id burned by a losing racer is simply discarded; it is never
// indexed, so no orphan payment is observable.
func (s *Store) Create(idemKey string, amountMinor int64, currency string) (paymentDTO, createResult) {
	now := time.Now().UTC().Format(time.RFC3339)
	bodyHash := normalizedBodyHash(amountMinor, currency)

	candidate := &Payment{
		ID:             newPaymentID(),
		AmountMinor:    amountMinor,
		Currency:       currency,
		Status:         StatusCreated,
		IdempotencyKey: idemKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	rec := &idemRecord{payment: candidate, bodyHash: bodyHash}

	actual, loaded := s.idem.LoadOrStore(idemKey, rec)
	if !loaded {
		// We won the race for this key. Publish by id as well.
		s.payments.Store(candidate.ID, candidate)
		return candidate.snapshot(), createdNew
	}

	existing := actual.(*idemRecord)
	if existing.bodyHash != bodyHash {
		return paymentDTO{}, conflictKeyReuse
	}
	return existing.payment.snapshot(), createdExisting
}

// Get returns the payment by id.
func (s *Store) Get(id string) (paymentDTO, bool) {
	v, ok := s.payments.Load(id)
	if !ok {
		return paymentDTO{}, false
	}
	return v.(*Payment).snapshot(), true
}

// Cancel transitions a payment to canceled (idempotently). Returns false if the
// payment does not exist.
func (s *Store) Cancel(id string) (paymentDTO, bool) {
	v, ok := s.payments.Load(id)
	if !ok {
		return paymentDTO{}, false
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return v.(*Payment).cancel(now), true
}
