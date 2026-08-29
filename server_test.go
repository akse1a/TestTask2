package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// helper: perform a request against a fresh server-backed test server.
func newTestServer() *httptest.Server {
	return httptest.NewServer(NewServer())
}

func doPost(t *testing.T, ts *httptest.Server, path, idemKey, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decodePayment(t *testing.T, resp *http.Response) paymentDTO {
	t.Helper()
	defer resp.Body.Close()
	var p paymentDTO
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode payment: %v", err)
	}
	return p
}

func decodeError(t *testing.T, resp *http.Response) errorBody {
	t.Helper()
	defer resp.Body.Close()
	var e errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	return e.Error
}

// §7.1 — create returns 201 and a valid object.
func TestCreatePayment(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp := doPost(t, ts, "/payments", "key-1", `{"amount_minor":10000,"currency":"RUB"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != contentTypeJSON {
		t.Fatalf("content-type = %q, want %q", ct, contentTypeJSON)
	}
	p := decodePayment(t, resp)
	if p.ID == "" {
		t.Fatal("id is empty")
	}
	if p.AmountMinor != 10000 || p.Currency != "RUB" {
		t.Fatalf("amount/currency mismatch: %+v", p)
	}
	if p.Status != StatusCreated {
		t.Fatalf("status = %q, want created", p.Status)
	}
	if p.IdempotencyKey != "key-1" {
		t.Fatalf("idempotency_key = %q", p.IdempotencyKey)
	}
	if p.CreatedAt == "" || p.UpdatedAt == "" {
		t.Fatal("timestamps missing")
	}
}

// §7.2 — repeat with same key+body -> 200, same id, no second payment.
func TestIdempotentRepeat(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	body := `{"currency":"USD","amount_minor":500}` // field order differs on purpose
	first := doPost(t, ts, "/payments", "key-2", body)
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", first.StatusCode)
	}
	p1 := decodePayment(t, first)

	// Repeat with reordered fields + extra whitespace -> normalized match.
	second := doPost(t, ts, "/payments", "key-2", `{ "amount_minor": 500 , "currency": "USD" }`)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", second.StatusCode)
	}
	p2 := decodePayment(t, second)

	if p1.ID != p2.ID {
		t.Fatalf("ids differ: %s vs %s", p1.ID, p2.ID)
	}
	if p1.CreatedAt != p2.CreatedAt {
		t.Fatalf("created_at differ: %s vs %s", p1.CreatedAt, p2.CreatedAt)
	}
}

// §7.3 — same key, different body -> 409 idempotency_key_reuse.
func TestKeyReuseDifferentBody(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	doPost(t, ts, "/payments", "key-3", `{"amount_minor":100,"currency":"RUB"}`).Body.Close()

	resp := doPost(t, ts, "/payments", "key-3", `{"amount_minor":200,"currency":"RUB"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if e := decodeError(t, resp); e.Code != codeIdempotencyKeyReuse {
		t.Fatalf("code = %q, want %q", e.Code, codeIdempotencyKeyReuse)
	}
}

// §7.4 — missing Idempotency-Key -> 400 missing_idempotency_key.
func TestMissingIdempotencyKey(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp := doPost(t, ts, "/payments", "", `{"amount_minor":100,"currency":"RUB"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if e := decodeError(t, resp); e.Code != codeMissingIdempotencyKey {
		t.Fatalf("code = %q, want %q", e.Code, codeMissingIdempotencyKey)
	}
}

// §7.5 — invalid amount and invalid currency -> 400.
func TestValidation(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"zero amount", `{"amount_minor":0,"currency":"RUB"}`, codeInvalidAmount},
		{"negative amount", `{"amount_minor":-5,"currency":"RUB"}`, codeInvalidAmount},
		{"fractional amount", `{"amount_minor":1.5,"currency":"RUB"}`, codeInvalidAmount},
		{"missing amount", `{"currency":"RUB"}`, codeInvalidAmount},
		{"bad currency", `{"amount_minor":100,"currency":"GBP"}`, codeInvalidCurrency},
		{"short currency", `{"amount_minor":100,"currency":"RU"}`, codeInvalidCurrency},
		{"missing currency", `{"amount_minor":100}`, codeInvalidCurrency},
		{"invalid json", `{not json`, codeInvalidJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doPost(t, ts, "/payments", "vkey-"+tc.name, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if e := decodeError(t, resp); e.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", e.Code, tc.wantCode)
			}
		})
	}
}

// §7.6 — GET existing/non-existing -> 200 / 404.
func TestGetPayment(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	created := decodePayment(t, doPost(t, ts, "/payments", "key-get", `{"amount_minor":700,"currency":"EUR"}`))

	// existing
	resp, err := http.Get(ts.URL + "/payments/" + created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodePayment(t, resp)
	if got.ID != created.ID {
		t.Fatalf("id = %q, want %q", got.ID, created.ID)
	}

	// non-existing
	resp2, err := http.Get(ts.URL + "/payments/pay_doesnotexist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp2.StatusCode)
	}
	if e := decodeError(t, resp2); e.Code != codePaymentNotFound {
		t.Fatalf("code = %q, want %q", e.Code, codePaymentNotFound)
	}
}

// §7.7 — cancel created -> canceled; repeat cancel -> 200 idempotent.
func TestCancel(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	created := decodePayment(t, doPost(t, ts, "/payments", "key-cancel", `{"amount_minor":900,"currency":"RUB"}`))

	first := doPost(t, ts, "/payments/"+created.ID+"/cancel", "", "")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first cancel status = %d, want 200", first.StatusCode)
	}
	p1 := decodePayment(t, first)
	if p1.Status != StatusCanceled {
		t.Fatalf("status = %q, want canceled", p1.Status)
	}

	// idempotent repeat
	second := doPost(t, ts, "/payments/"+created.ID+"/cancel", "", "")
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second cancel status = %d, want 200", second.StatusCode)
	}
	p2 := decodePayment(t, second)
	if p2.Status != StatusCanceled {
		t.Fatalf("status = %q, want canceled", p2.Status)
	}
	if p1.ID != p2.ID {
		t.Fatalf("ids differ")
	}

	// cancel non-existing -> 404
	nf := doPost(t, ts, "/payments/pay_missing/cancel", "", "")
	if nf.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", nf.StatusCode)
	}
	if e := decodeError(t, nf); e.Code != codePaymentNotFound {
		t.Fatalf("code = %q, want %q", e.Code, codePaymentNotFound)
	}
}

// §7.8 — concurrency: N goroutines with one key -> exactly one payment.
func TestConcurrentCreateSingleKey(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	const n = 64
	var wg sync.WaitGroup
	var created201 int64

	ids := make(chan string, n)
	body := `{"amount_minor":4242,"currency":"RUB"}`

	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all at once to maximize contention
			resp := doPost(t, ts, "/payments", "race-key", body)
			if resp.StatusCode == http.StatusCreated {
				atomic.AddInt64(&created201, 1)
			} else if resp.StatusCode != http.StatusOK {
				t.Errorf("unexpected status %d", resp.StatusCode)
			}
			p := decodePayment(t, resp)
			ids <- p.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)

	if created201 != 1 {
		t.Fatalf("got %d 201-responses, want exactly 1", created201)
	}

	uniq := map[string]struct{}{}
	for id := range ids {
		uniq[id] = struct{}{}
	}
	if len(uniq) != 1 {
		t.Fatalf("got %d distinct ids, want 1: %v", len(uniq), uniq)
	}
}

// method_not_allowed on a known path (SPEC §4).
func TestMethodNotAllowed(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/payments", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if e := decodeError(t, resp); e.Code != codeMethodNotAllowed {
		t.Fatalf("code = %q, want %q", e.Code, codeMethodNotAllowed)
	}
}

// healthz liveness (SPEC §2.4).
func TestHealthz(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %q, want ok", got["status"])
	}
}
