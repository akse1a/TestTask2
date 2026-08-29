package main

import (
	"encoding/json"
	"io"
	"net/http"
)

// Error codes (SPEC §4).
const (
	codeMissingIdempotencyKey = "missing_idempotency_key"
	codeInvalidJSON           = "invalid_json"
	codeInvalidAmount         = "invalid_amount"
	codeInvalidCurrency       = "invalid_currency"
	codeIdempotencyKeyReuse   = "idempotency_key_reuse"
	codePaymentNotFound       = "payment_not_found"
	codeMethodNotAllowed      = "method_not_allowed"
	codeInternalError         = "internal_error"
)

const contentTypeJSON = "application/json; charset=utf-8"

var supportedCurrencies = map[string]struct{}{
	"RUB": {},
	"USD": {},
	"EUR": {},
}

// errorEnvelope is the single error wrapper for all failures (SPEC §4).
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Server holds dependencies and builds the router.
type Server struct {
	store *Store
	mux   *http.ServeMux
}

func NewServer() *Server {
	s := &Server{store: NewStore(), mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// Patterns are registered without methods so we can emit our own JSON
	// method_not_allowed envelope instead of the mux's empty-bodied 405.
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/payments", s.handlePayments)
	s.mux.HandleFunc("/payments/{id}", s.handlePaymentByID)
	s.mux.HandleFunc("/payments/{id}/cancel", s.handleCancel)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	// Encoding a plain DTO/envelope cannot fail; if the client disconnects the
	// write error is not actionable here.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
		return
	}
	s.createPayment(w, r)
}

func (s *Server) handlePaymentByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
		return
	}
	id := r.PathValue("id")
	p, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, codePaymentNotFound, "payment not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
		return
	}
	id := r.PathValue("id")
	p, ok := s.store.Cancel(id)
	if !ok {
		writeError(w, http.StatusNotFound, codePaymentNotFound, "payment not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	// 1. Idempotency-Key header: required, 1..255 chars (SPEC §2.1).
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" || len(idemKey) > 255 {
		writeError(w, http.StatusBadRequest, codeMissingIdempotencyKey, "Idempotency-Key header is required (1-255 chars)")
		return
	}

	// 2. Body must be valid JSON.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return
	}

	// 3. amount_minor: present, integer, > 0.
	amountMinor, ok := parseAmount(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, codeInvalidAmount, "amount_minor must be an integer > 0")
		return
	}

	// 4. currency: exactly 3 letters, one of RUB/USD/EUR.
	currency, ok := parseCurrency(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, codeInvalidCurrency, "currency must be one of RUB, USD, EUR")
		return
	}

	// 5. Idempotent create.
	dto, result := s.store.Create(idemKey, amountMinor, currency)
	switch result {
	case createdNew:
		writeJSON(w, http.StatusCreated, dto)
	case createdExisting:
		writeJSON(w, http.StatusOK, dto)
	case conflictKeyReuse:
		writeError(w, http.StatusConflict, codeIdempotencyKeyReuse, "Idempotency-Key reused with a different body")
	default:
		writeError(w, http.StatusInternalServerError, codeInternalError, "unexpected error")
	}
}

// parseAmount extracts a strictly-positive integer amount_minor. It rejects a
// missing field, non-numeric JSON, and non-integer numbers (e.g. 1.5).
func parseAmount(raw map[string]json.RawMessage) (int64, bool) {
	amtRaw, present := raw["amount_minor"]
	if !present {
		return 0, false
	}
	var num json.Number
	if err := json.Unmarshal(amtRaw, &num); err != nil {
		return 0, false
	}
	amt, err := num.Int64() // fails for fractional numbers like 1.5
	if err != nil {
		return 0, false
	}
	if amt <= 0 {
		return 0, false
	}
	return amt, true
}

// parseCurrency extracts a supported ISO-4217 currency code.
func parseCurrency(raw map[string]json.RawMessage) (string, bool) {
	curRaw, present := raw["currency"]
	if !present {
		return "", false
	}
	var cur string
	if err := json.Unmarshal(curRaw, &cur); err != nil {
		return "", false
	}
	if len(cur) != 3 {
		return "", false
	}
	if _, ok := supportedCurrencies[cur]; !ok {
		return "", false
	}
	return cur, true
}
