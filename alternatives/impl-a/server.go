package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Server is the HTTP handler for the payment API. It holds the single store.
type Server struct {
	store *Store
	mux   *http.ServeMux
}

// NewServer builds a Server with an empty in-memory store and routing wired up.
func NewServer() *Server {
	s := &Server{
		store: NewStore(),
		mux:   http.NewServeMux(),
	}
	// We route mostly by hand (below) so we can produce precise 404/405 and
	// error codes, but expose ServeHTTP directly.
	return s
}

// Error codes (SPEC §4).
const (
	codeMissingKey  = "missing_idempotency_key"
	codeInvalidJSON = "invalid_json"
	codeInvalidAmt  = "invalid_amount"
	codeInvalidCurr = "invalid_currency"
	codeKeyReuse    = "idempotency_key_reuse"
	codeNotFound    = "payment_not_found"
	codeMethodNotOK = "method_not_allowed"
	codeInternalErr = "internal_error"
)

// errorEnvelope is the single error shape for all errors (SPEC §4).
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP dispatches requests. Routing is explicit so that unsupported
// methods on a known path return 405 method_not_allowed rather than 404.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case path == "/healthz":
		s.handleHealthz(w, r)

	case path == "/payments":
		s.handlePaymentsRoot(w, r)

	case strings.HasPrefix(path, "/payments/"):
		s.handlePaymentByID(w, r, strings.TrimPrefix(path, "/payments/"))

	default:
		writeError(w, http.StatusNotFound, codeNotFound, "resource not found")
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotOK, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePaymentsRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotOK, "method not allowed")
		return
	}
	s.handleCreate(w, r)
}

// handlePaymentByID handles /payments/{id} and /payments/{id}/cancel.
func (s *Server) handlePaymentByID(w http.ResponseWriter, r *http.Request, rest string) {
	// rest is everything after "/payments/". It is either "{id}" or
	// "{id}/cancel".
	if strings.HasSuffix(rest, "/cancel") {
		id := strings.TrimSuffix(rest, "/cancel")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, codeNotFound, "payment not found")
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, codeMethodNotOK, "method not allowed")
			return
		}
		s.handleCancel(w, r, id)
		return
	}

	id := rest
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, codeNotFound, "payment not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotOK, "method not allowed")
		return
	}
	s.handleGet(w, r, id)
}

// createReq is the raw request body. AmountMinor is kept as RawMessage so we can
// tell "absent" from "present but not a positive integer" and reject floats.
type createReq struct {
	AmountMinor json.RawMessage `json:"amount_minor"`
	Currency    *string         `json:"currency"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, codeMissingKey, "Idempotency-Key header is required")
		return
	}
	if len(key) > 255 {
		// SPEC §2.1 constrains the key to 1–255 chars. There is no dedicated
		// code for "too long", so we reuse missing_idempotency_key as the
		// closest fit (documented in NOTES.md).
		writeError(w, http.StatusBadRequest, codeMissingKey, "Idempotency-Key must be 1–255 characters")
		return
	}

	var req createReq
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return
	}

	amount, ok := parseAmountMinor(req.AmountMinor)
	if !ok {
		writeError(w, http.StatusBadRequest, codeInvalidAmt, "amount_minor must be an integer > 0")
		return
	}

	if req.Currency == nil || !validCurrency(*req.Currency) {
		writeError(w, http.StatusBadRequest, codeInvalidCurr, "currency must be one of RUB, USD, EUR")
		return
	}

	payment, result := s.store.CreateIdempotent(key, amount, *req.Currency)
	switch result {
	case createdNew:
		writeJSON(w, http.StatusCreated, payment)
	case createdExisting:
		writeJSON(w, http.StatusOK, payment)
	case createdConflict:
		writeError(w, http.StatusConflict, codeKeyReuse,
			"Idempotency-Key was already used with a different request body")
	default:
		writeError(w, http.StatusInternalServerError, codeInternalErr, "unexpected error")
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "payment not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := s.store.Cancel(id)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "payment not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// parseAmountMinor validates that raw is a JSON integer strictly greater than
// zero. Floats (e.g. 1.5), absent values, null, strings, and non-positive
// integers are all rejected (SPEC §2.1).
func parseAmountMinor(raw json.RawMessage) (int64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	// A JSON integer has no '.', 'e' or 'E'. ParseInt rejects those anyway, but
	// we guard explicitly for clarity.
	if strings.ContainsAny(s, ".eE") {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// validCurrency reports whether c is exactly 3 uppercase A-Z letters and is one
// of the supported currencies (SPEC §1).
func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return supportedCurrencies[c]
}

// writeJSON writes v as JSON with the given status and the required content type.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard error envelope (SPEC §4).
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}
