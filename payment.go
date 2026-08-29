package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Payment statuses (SPEC §3).
const (
	StatusCreated  = "created"
	StatusCanceled = "canceled"
)

// Payment is the internal, mutable representation of a payment.
//
// The mutex guards the fields that can change after creation (Status,
// UpdatedAt). All other fields are set once at construction and are then
// effectively immutable, so the creation hot path never needs a lock — it
// simply publishes a fully-built *Payment through sync.Map (see store.go).
type Payment struct {
	mu sync.Mutex

	ID             string
	AmountMinor    int64
	Currency       string
	Status         string
	IdempotencyKey string
	CreatedAt      string
	UpdatedAt      string
}

// paymentDTO is the wire representation (snake_case, no mutex).
type paymentDTO struct {
	ID             string `json:"id"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// snapshot returns a consistent copy of the payment for serialization.
func (p *Payment) snapshot() paymentDTO {
	p.mu.Lock()
	defer p.mu.Unlock()
	return paymentDTO{
		ID:             p.ID,
		AmountMinor:    p.AmountMinor,
		Currency:       p.Currency,
		Status:         p.Status,
		IdempotencyKey: p.IdempotencyKey,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

// cancel moves the payment to canceled. It is idempotent: cancelling an
// already-canceled payment is a no-op and not an error. Returns a snapshot
// taken atomically with the transition.
func (p *Payment) cancel(now string) paymentDTO {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Status == StatusCreated {
		p.Status = StatusCanceled
		p.UpdatedAt = now
	}
	return paymentDTO{
		ID:             p.ID,
		AmountMinor:    p.AmountMinor,
		Currency:       p.Currency,
		Status:         p.Status,
		IdempotencyKey: p.IdempotencyKey,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

// newPaymentID generates a unique, unpredictable payment id like "pay_<hex>".
func newPaymentID() string {
	var b [16]byte
	// crypto/rand.Read never returns a short read and only errors if the
	// system entropy source is unavailable, which is fatal for the process.
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "pay_" + hex.EncodeToString(b[:])
}
