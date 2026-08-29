// Package handlers is the HTTP transport layer. It parses and validates
// requests, delegates to the store, and renders every response (including the
// unified error envelope from SPEC §4). Routing is done by a small explicit
// router so that the 404-vs-405 distinction and the JSON error bodies are fully
// under our control.
package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"paymentapi/apperr"
	"paymentapi/store"
)

const maxBodyBytes = 1 << 20 // 1 MiB request-body cap

// supportedCurrencies is the fixed set from SPEC §1/§2.1.
var supportedCurrencies = map[string]bool{"RUB": true, "USD": true, "EUR": true}

// API bundles the dependencies of the HTTP layer.
type API struct {
	store *store.Store
}

// New returns an API backed by the given store.
func New(s *store.Store) *API { return &API{store: s} }

// ServeHTTP is the explicit router. Paths are matched exactly; a known path with
// an unsupported method yields 405 method_not_allowed (SPEC §4), while unknown
// paths yield a plain 404.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case path == "/healthz":
		if r.Method != http.MethodGet {
			writeError(w, apperr.ErrMethodNotAllowed)
			return
		}
		a.handleHealth(w, r)

	case path == "/payments":
		if r.Method != http.MethodPost {
			writeError(w, apperr.ErrMethodNotAllowed)
			return
		}
		a.handleCreate(w, r)

	case strings.HasPrefix(path, "/payments/"):
		rest := strings.TrimPrefix(path, "/payments/")
		if id, ok := strings.CutSuffix(rest, "/cancel"); ok {
			if id == "" {
				writeError(w, apperr.ErrPaymentNotFound)
				return
			}
			if r.Method != http.MethodPost {
				writeError(w, apperr.ErrMethodNotAllowed)
				return
			}
			a.handleCancel(w, r, id)
			return
		}
		// /payments/{id}
		if rest == "" || strings.Contains(rest, "/") {
			// e.g. "/payments/" or "/payments/a/b" — not a defined resource.
			writeError(w, apperr.ErrPaymentNotFound)
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, apperr.ErrMethodNotAllowed)
			return
		}
		a.handleGet(w, r, rest)

	default:
		writeError(w, apperr.ErrNotFound)
	}
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleCreate(w http.ResponseWriter, r *http.Request) {
	// Idempotency-Key is mandatory and bounded to 1..255 characters, i.e. Unicode
	// code points, not bytes (SPEC §2.1). RuneCountInString keeps a valid
	// multi-byte key (>255 bytes but <=255 runes) from being wrongly rejected.
	key := r.Header.Get("Idempotency-Key")
	if key == "" || utf8.RuneCountInString(key) > 255 {
		writeError(w, apperr.ErrMissingIdempotencyKey)
		return
	}

	amount, currency, aerr := parseCreateBody(r.Body)
	if aerr != nil {
		writeError(w, aerr)
		return
	}

	p, created, err := a.store.CreatePayment(key, amount, currency)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, p)
}

func (a *API) handleGet(w http.ResponseWriter, _ *http.Request, id string) {
	p, _, err := a.store.GetPayment(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *API) handleCancel(w http.ResponseWriter, _ *http.Request, id string) {
	p, err := a.store.CancelPayment(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// parseCreateBody reads and validates the POST /payments body. It returns the
// normalized (amount, currency) pair or a typed 400 error. Validation is done
// per field on top of a first JSON-syntax check so that a well-formed JSON with
// a bad field yields the specific code (invalid_amount / invalid_currency)
// rather than invalid_json.
func parseCreateBody(body io.Reader) (int64, string, *apperr.Error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBodyBytes))
	if err != nil {
		return 0, "", apperr.ErrInvalidJSON
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, "", apperr.ErrInvalidJSON
	}

	// amount_minor: must be present and a positive integer.
	amtRaw, ok := raw["amount_minor"]
	if !ok {
		return 0, "", apperr.ErrInvalidAmount
	}
	// Read exactly one JSON token with number-preserving semantics. This
	// rejects a quoted value like "100" (which decodes to a Go string, not a
	// json.Number) as well as booleans/objects — all of which must be
	// invalid_amount rather than silently accepted.
	dec := json.NewDecoder(bytes.NewReader(amtRaw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return 0, "", apperr.ErrInvalidAmount
	}
	num, ok := tok.(json.Number)
	if !ok {
		return 0, "", apperr.ErrInvalidAmount // string/bool/null instead of a number
	}
	amount, err := num.Int64()
	if err != nil {
		return 0, "", apperr.ErrInvalidAmount // non-integer number, e.g. 1.5
	}
	if amount <= 0 {
		return 0, "", apperr.ErrInvalidAmount
	}

	// currency: must be present and one of the supported ISO-4217 codes.
	curRaw, ok := raw["currency"]
	if !ok {
		return 0, "", apperr.ErrInvalidCurrency
	}
	var currency string
	if err := json.Unmarshal(curRaw, &currency); err != nil {
		return 0, "", apperr.ErrInvalidCurrency
	}
	if !supportedCurrencies[currency] {
		return 0, "", apperr.ErrInvalidCurrency
	}

	return amount, currency, nil
}

// writeJSON renders v as UTF-8 JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorEnvelope is the unified error wrapper from SPEC §4.
type errorEnvelope struct {
	Error *apperr.Error `json:"error"`
}

// writeError renders any error as the SPEC §4 envelope. Typed domain errors keep
// their status and code; anything else is coerced to a 500 internal_error so we
// never leak an untyped error to the client.
func writeError(w http.ResponseWriter, err error) {
	ae, ok := err.(*apperr.Error)
	if !ok {
		ae = apperr.ErrInternal
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: ae})
}
