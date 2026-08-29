// Package apperr defines the typed domain errors used across the service and
// maps each of them to a fixed HTTP status and a stable error code. Keeping the
// definitions in one place guarantees that the wire format (SPEC §4) and the
// status/code table stay consistent everywhere they are produced.
package apperr

import "net/http"

// Error is a typed domain error. It carries the HTTP status it should be
// rendered with, the stable machine-readable code from SPEC §4, and a
// human-readable message. It implements the error interface so it can flow
// through normal Go error returns from the store up to the handlers.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New builds a domain error. Used both for the shared sentinels below and for
// per-request errors that need a custom message.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Shared sentinels. These cover every error code enumerated in SPEC §4.
var (
	ErrMissingIdempotencyKey = New(http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is required (1-255 chars)")
	ErrInvalidJSON           = New(http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
	ErrInvalidAmount         = New(http.StatusBadRequest, "invalid_amount", "amount_minor must be an integer > 0")
	ErrInvalidCurrency       = New(http.StatusBadRequest, "invalid_currency", "currency must be one of RUB, USD, EUR")
	ErrIdempotencyKeyReuse   = New(http.StatusConflict, "idempotency_key_reuse", "Idempotency-Key already used with a different request body")
	ErrPaymentNotFound       = New(http.StatusNotFound, "payment_not_found", "payment not found")
	ErrMethodNotAllowed      = New(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed for this path")
	ErrNotFound              = New(http.StatusNotFound, "not_found", "resource not found")
	ErrInternal              = New(http.StatusInternalServerError, "internal_error", "internal server error")
)
