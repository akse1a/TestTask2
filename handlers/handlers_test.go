package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"paymentapi/handlers"
	"paymentapi/store"
)

// newServer spins up an httptest server backed by a fresh store.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handlers.New(store.New()))
	t.Cleanup(srv.Close)
	return srv
}

type paymentResp struct {
	ID             string `json:"id"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type errResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// post performs POST /payments with the given idempotency key and raw body.
func post(t *testing.T, base, key, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/payments", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req.Header.Set("Content-Type", "application/json")
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, buf.Bytes()
}

func decodePayment(t *testing.T, b []byte) paymentResp {
	t.Helper()
	var p paymentResp
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode payment: %v (body=%s)", err, b)
	}
	return p
}

func decodeErr(t *testing.T, b []byte) errResp {
	t.Helper()
	var e errResp
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("decode error: %v (body=%s)", err, b)
	}
	return e
}

// SPEC §7.1 — create returns 201 and a valid object.
func TestCreateReturns201(t *testing.T) {
	srv := newServer(t)
	resp, body := post(t, srv.URL, "key-1", `{"amount_minor":10000,"currency":"RUB"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	p := decodePayment(t, body)
	if p.ID == "" {
		t.Fatal("empty id")
	}
	if p.AmountMinor != 10000 || p.Currency != "RUB" {
		t.Fatalf("bad amount/currency: %+v", p)
	}
	if p.Status != store.StatusCreated {
		t.Fatalf("status = %q, want created", p.Status)
	}
	if p.IdempotencyKey != "key-1" {
		t.Fatalf("idempotency_key = %q", p.IdempotencyKey)
	}
	if p.CreatedAt == "" || p.UpdatedAt == "" {
		t.Fatalf("missing timestamps: %+v", p)
	}
}

// SPEC §7.2 — repeat with same key and body → 200, same id, no second payment.
func TestIdempotentReplaySameBody(t *testing.T) {
	srv := newServer(t)
	r1, b1 := post(t, srv.URL, "key-2", `{"amount_minor":500,"currency":"USD"}`)
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", r1.StatusCode)
	}
	p1 := decodePayment(t, b1)

	// Same pair, different field order and whitespace — must normalize equal.
	r2, b2 := post(t, srv.URL, "key-2", `{ "currency":"USD" ,  "amount_minor": 500 }`)
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200 (body=%s)", r2.StatusCode, b2)
	}
	p2 := decodePayment(t, b2)
	if p1.ID != p2.ID {
		t.Fatalf("ids differ: %q vs %q", p1.ID, p2.ID)
	}
	if p1.CreatedAt != p2.CreatedAt {
		t.Fatalf("created_at differ: %q vs %q", p1.CreatedAt, p2.CreatedAt)
	}
}

// SPEC §7.3 — same key, different body → 409 idempotency_key_reuse.
func TestIdempotencyKeyReuseConflict(t *testing.T) {
	srv := newServer(t)
	if r, _ := post(t, srv.URL, "key-3", `{"amount_minor":100,"currency":"RUB"}`); r.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d", r.StatusCode)
	}
	r, b := post(t, srv.URL, "key-3", `{"amount_minor":200,"currency":"RUB"}`)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", r.StatusCode, b)
	}
	if code := decodeErr(t, b).Error.Code; code != "idempotency_key_reuse" {
		t.Fatalf("code = %q, want idempotency_key_reuse", code)
	}
}

// SPEC §7.4 — missing Idempotency-Key → 400 missing_idempotency_key.
func TestMissingIdempotencyKey(t *testing.T) {
	srv := newServer(t)
	r, b := post(t, srv.URL, "", `{"amount_minor":100,"currency":"RUB"}`)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", r.StatusCode, b)
	}
	if code := decodeErr(t, b).Error.Code; code != "missing_idempotency_key" {
		t.Fatalf("code = %q, want missing_idempotency_key", code)
	}
}

// SPEC §7.5 — invalid amount and invalid currency.
func TestValidationErrors(t *testing.T) {
	srv := newServer(t)
	cases := []struct {
		name, body, wantCode string
	}{
		{"zero amount", `{"amount_minor":0,"currency":"RUB"}`, "invalid_amount"},
		{"negative amount", `{"amount_minor":-5,"currency":"RUB"}`, "invalid_amount"},
		{"missing amount", `{"currency":"RUB"}`, "invalid_amount"},
		{"non-integer amount", `{"amount_minor":1.5,"currency":"RUB"}`, "invalid_amount"},
		{"string amount", `{"amount_minor":"100","currency":"RUB"}`, "invalid_amount"},
		{"bad currency", `{"amount_minor":100,"currency":"GBP"}`, "invalid_currency"},
		{"short currency", `{"amount_minor":100,"currency":"RU"}`, "invalid_currency"},
		{"missing currency", `{"amount_minor":100}`, "invalid_currency"},
		{"invalid json", `{not json`, "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, b := post(t, srv.URL, "k-"+tc.name, tc.body)
			if r.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", r.StatusCode, b)
			}
			if code := decodeErr(t, b).Error.Code; code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// SPEC §7.6 — GET existing → 200, GET missing → 404.
func TestGetPayment(t *testing.T) {
	srv := newServer(t)
	_, b := post(t, srv.URL, "key-get", `{"amount_minor":777,"currency":"EUR"}`)
	id := decodePayment(t, b).ID

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/payments/"+id, nil)
	r, gb := do(t, req)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("get existing status = %d, want 200", r.StatusCode)
	}
	if got := decodePayment(t, gb); got.ID != id || got.AmountMinor != 777 {
		t.Fatalf("unexpected payment: %+v", got)
	}

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/payments/pay_nope", nil)
	r2, gb2 := do(t, req2)
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("get missing status = %d, want 404", r2.StatusCode)
	}
	if code := decodeErr(t, gb2).Error.Code; code != "payment_not_found" {
		t.Fatalf("code = %q, want payment_not_found", code)
	}
}

// SPEC §7.7 — cancel created → canceled; repeat cancel → 200 idempotent.
func TestCancelPayment(t *testing.T) {
	srv := newServer(t)
	_, b := post(t, srv.URL, "key-cancel", `{"amount_minor":300,"currency":"RUB"}`)
	p := decodePayment(t, b)

	cancel := func() paymentResp {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/payments/"+p.ID+"/cancel", nil)
		r, cb := do(t, req)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("cancel status = %d, want 200 (body=%s)", r.StatusCode, cb)
		}
		return decodePayment(t, cb)
	}

	c1 := cancel()
	if c1.Status != store.StatusCanceled {
		t.Fatalf("status = %q, want canceled", c1.Status)
	}
	if c1.UpdatedAt < p.CreatedAt {
		t.Fatalf("updated_at not advanced: %q < %q", c1.UpdatedAt, p.CreatedAt)
	}

	c2 := cancel() // idempotent second cancel
	if c2.Status != store.StatusCanceled {
		t.Fatalf("second cancel status = %q, want canceled", c2.Status)
	}
	if c2.ID != c1.ID {
		t.Fatalf("id changed on re-cancel")
	}

	// Cancel a missing payment → 404.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/payments/pay_missing/cancel", nil)
	r, cb := do(t, req)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel missing status = %d, want 404", r.StatusCode)
	}
	if code := decodeErr(t, cb).Error.Code; code != "payment_not_found" {
		t.Fatalf("code = %q, want payment_not_found", code)
	}
}

// SPEC §7.8 — concurrency: N goroutines, one key, exactly one payment created.
// Run with -race. Exactly one response is 201; every other is 200 with the same id.
func TestConcurrentSameKeyCreatesOnePayment(t *testing.T) {
	srv := newServer(t)
	const n = 64
	const key = "concurrent-key"
	const body = `{"amount_minor":4200,"currency":"RUB"}`

	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := make(map[string]struct{})
	statusCount := map[int]int{}

	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once to maximize contention
			r, b := post(t, srv.URL, key, body)
			p := decodePayment(t, b)
			mu.Lock()
			ids[p.ID] = struct{}{}
			statusCount[r.StatusCode]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if len(ids) != 1 {
		t.Fatalf("got %d distinct ids, want exactly 1: %v", len(ids), ids)
	}
	if statusCount[http.StatusCreated] != 1 {
		t.Fatalf("got %d 201 responses, want exactly 1 (counts=%v)", statusCount[http.StatusCreated], statusCount)
	}
	if statusCount[http.StatusOK] != n-1 {
		t.Fatalf("got %d 200 responses, want %d (counts=%v)", statusCount[http.StatusOK], n-1, statusCount)
	}
}

// SPEC §4 — unsupported method on a known path → 405 method_not_allowed.
func TestMethodNotAllowed(t *testing.T) {
	srv := newServer(t)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/payments", nil)
	r, b := do(t, req)
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", r.StatusCode)
	}
	if code := decodeErr(t, b).Error.Code; code != "method_not_allowed" {
		t.Fatalf("code = %q, want method_not_allowed", code)
	}
}

// SPEC §2.4 — healthz.
func TestHealthz(t *testing.T) {
	srv := newServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/healthz", nil)
	r, b := do(t, req)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || m["status"] != "ok" {
		t.Fatalf("body = %s, want {\"status\":\"ok\"}", b)
	}
}
