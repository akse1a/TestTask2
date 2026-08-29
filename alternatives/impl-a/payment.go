package main

// Payment is the domain object described in SPEC §1. Money is always stored as
// integer minor units (AmountMinor); no floats are ever used for amounts.
type Payment struct {
	ID             string `json:"id"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// Statuses (SPEC §3). created is the only start status; canceled is terminal.
// The only allowed transition is created -> canceled.
const (
	StatusCreated  = "created"
	StatusCanceled = "canceled"
)

// Supported ISO-4217 currencies (SPEC §1).
var supportedCurrencies = map[string]bool{
	"RUB": true,
	"USD": true,
	"EUR": true,
}
