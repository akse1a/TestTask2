package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// newTestServer returns a running httptest.Server backed by a fresh store.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewServer())
	t.Cleanup(ts.Close)
	return ts
}

// doJSON issues a request with optional Idempotency-Key and JSON body and
// decodes the response into out (if non-nil). It returns the status code.
func doJSON(t *testing.T, method, url, idemKey, body string, out interface{}) int {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return resp.StatusCode
}

func errCode(t *testing.T, raw map[string]json.RawMessage) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &env)
	return env.Error.Code
}

// §7.1 — create returns 201 and a valid object.
func TestCreate_Returns201AndValidObject(t *testing.T) {
	ts := newTestServer(t)
	var p Payment
	status := doJSON(t, http.MethodPost, ts.URL+"/payments", "key-1",
		`{"amount_minor":10000,"currency":"RUB"}`, &p)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if p.ID == "" || !strings.HasPrefix(p.ID, "pay_") {
		t.Errorf("id = %q, want non-empty pay_ prefix", p.ID)
	}
	if p.AmountMinor != 10000 || p.Currency != "RUB" {
		t.Errorf("body mismatch: %+v", p)
	}
	if p.Status != StatusCreated {
		t.Errorf("status field = %q, want %q", p.Status, StatusCreated)
	}
	if p.IdempotencyKey != "key-1" {
		t.Errorf("idempotency_key = %q, want key-1", p.IdempotencyKey)
	}
	if p.CreatedAt == "" || p.UpdatedAt == "" {
		t.Errorf("timestamps must be set: %+v", p)
	}
}

// §7.2 — replay with same key and body returns 200, same id, no second payment.
func TestCreate_IdempotentReplay(t *testing.T) {
	ts := newTestServer(t)
	var first, second Payment
	s1 := doJSON(t, http.MethodPost, ts.URL+"/payments", "key-2",
		`{"amount_minor":500,"currency":"USD"}`, &first)
	if s1 != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", s1)
	}
	// Same body, different field order / whitespace — must still match.
	s2 := doJSON(t, http.MethodPost, ts.URL+"/payments", "key-2",
		`{ "currency":"USD" ,  "amount_minor": 500 }`, &second)
	if s2 != http.StatusOK {
		t.Fatalf("second status = %d, want 200", s2)
	}
	if first.ID != second.ID {
		t.Errorf("ids differ: %q vs %q", first.ID, second.ID)
	}
	if first.CreatedAt != second.CreatedAt {
		t.Errorf("created_at differ: %q vs %q", first.CreatedAt, second.CreatedAt)
	}
}

// §7.3 — same key, different body returns 409 idempotency_key_reuse.
func TestCreate_KeyReuseConflict(t *testing.T) {
	ts := newTestServer(t)
	_ = doJSON(t, http.MethodPost, ts.URL+"/payments", "key-3",
		`{"amount_minor":500,"currency":"USD"}`, nil)
	var raw map[string]json.RawMessage
	status := doJSON(t, http.MethodPost, ts.URL+"/payments", "key-3",
		`{"amount_minor":999,"currency":"USD"}`, &raw)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if c := errCode(t, raw); c != codeKeyReuse {
		t.Errorf("code = %q, want %q", c, codeKeyReuse)
	}
}

// §7.4 — missing Idempotency-Key returns 400 missing_idempotency_key.
func TestCreate_MissingKey(t *testing.T) {
	ts := newTestServer(t)
	var raw map[string]json.RawMessage
	status := doJSON(t, http.MethodPost, ts.URL+"/payments", "",
		`{"amount_minor":500,"currency":"USD"}`, &raw)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if c := errCode(t, raw); c != codeMissingKey {
		t.Errorf("code = %q, want %q", c, codeMissingKey)
	}
}

// §7.5 — amount_minor <= 0 returns 400 invalid_amount; bad currency returns
// 400 invalid_currency. Also covers float and missing amount.
func TestCreate_ValidationErrors(t *testing.T) {
	ts := newTestServer(t)
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"zero amount", `{"amount_minor":0,"currency":"RUB"}`, codeInvalidAmt},
		{"negative amount", `{"amount_minor":-5,"currency":"RUB"}`, codeInvalidAmt},
		{"float amount", `{"amount_minor":1.5,"currency":"RUB"}`, codeInvalidAmt},
		{"missing amount", `{"currency":"RUB"}`, codeInvalidAmt},
		{"unsupported currency", `{"amount_minor":10,"currency":"GBP"}`, codeInvalidCurr},
		{"malformed currency", `{"amount_minor":10,"currency":"ru"}`, codeInvalidCurr},
		{"missing currency", `{"amount_minor":10}`, codeInvalidCurr},
		{"invalid json", `{not json`, codeInvalidJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			status := doJSON(t, http.MethodPost, ts.URL+"/payments", "vk-"+tc.name, tc.body, &raw)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if c := errCode(t, raw); c != tc.wantCode {
				t.Errorf("code = %q, want %q", c, tc.wantCode)
			}
		})
	}
}

// §7.6 — GET existing returns 200; GET missing returns 404 payment_not_found.
func TestGet_ExistingAndMissing(t *testing.T) {
	ts := newTestServer(t)
	var created Payment
	doJSON(t, http.MethodPost, ts.URL+"/payments", "key-get",
		`{"amount_minor":42,"currency":"EUR"}`, &created)

	var got Payment
	if s := doJSON(t, http.MethodGet, ts.URL+"/payments/"+created.ID, "", "", &got); s != http.StatusOK {
		t.Fatalf("get existing status = %d, want 200", s)
	}
	if got.ID != created.ID {
		t.Errorf("got id %q, want %q", got.ID, created.ID)
	}

	var raw map[string]json.RawMessage
	if s := doJSON(t, http.MethodGet, ts.URL+"/payments/pay_missing", "", "", &raw); s != http.StatusNotFound {
		t.Fatalf("get missing status = %d, want 404", s)
	}
	if c := errCode(t, raw); c != codeNotFound {
		t.Errorf("code = %q, want %q", c, codeNotFound)
	}
}

// §7.7 — cancel created -> canceled; repeated cancel is idempotent 200.
func TestCancel_Idempotent(t *testing.T) {
	ts := newTestServer(t)
	var created Payment
	doJSON(t, http.MethodPost, ts.URL+"/payments", "key-cancel",
		`{"amount_minor":42,"currency":"EUR"}`, &created)

	var c1 Payment
	if s := doJSON(t, http.MethodPost, ts.URL+"/payments/"+created.ID+"/cancel", "", "", &c1); s != http.StatusOK {
		t.Fatalf("first cancel status = %d, want 200", s)
	}
	if c1.Status != StatusCanceled {
		t.Errorf("status = %q, want canceled", c1.Status)
	}
	if c1.UpdatedAt == "" {
		t.Errorf("updated_at should be set")
	}

	var c2 Payment
	if s := doJSON(t, http.MethodPost, ts.URL+"/payments/"+created.ID+"/cancel", "", "", &c2); s != http.StatusOK {
		t.Fatalf("second cancel status = %d, want 200", s)
	}
	if c2.Status != StatusCanceled || c2.ID != c1.ID {
		t.Errorf("second cancel mismatch: %+v", c2)
	}

	// Cancel a non-existent payment -> 404.
	var raw map[string]json.RawMessage
	if s := doJSON(t, http.MethodPost, ts.URL+"/payments/pay_nope/cancel", "", "", &raw); s != http.StatusNotFound {
		t.Fatalf("cancel missing status = %d, want 404", s)
	}
	if c := errCode(t, raw); c != codeNotFound {
		t.Errorf("code = %q, want %q", c, codeNotFound)
	}
}

// §7.8 — concurrency: N goroutines with one key produce exactly one payment.
func TestCreate_ConcurrentSameKey(t *testing.T) {
	srv := NewServer()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const n = 64
	var wg sync.WaitGroup
	ids := make([]string, n)
	statuses := make([]int, n)
	body := `{"amount_minor":777,"currency":"RUB"}`

	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines as simultaneously as possible
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/payments", bytes.NewReader([]byte(body)))
			req.Header.Set("Idempotency-Key", "concurrent-key")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			defer resp.Body.Close()
			var p Payment
			_ = json.NewDecoder(resp.Body).Decode(&p)
			ids[i] = p.ID
			statuses[i] = resp.StatusCode
		}(i)
	}
	close(start)
	wg.Wait()

	// Exactly one payment must exist in the store.
	if got := srv.store.Count(); got != 1 {
		t.Fatalf("store has %d payments, want exactly 1", got)
	}

	// All responses must reference the same id, and exactly one must be 201.
	first := ids[0]
	created201 := 0
	for i := 0; i < n; i++ {
		if ids[i] != first {
			t.Errorf("response %d id = %q, want %q", i, ids[i], first)
		}
		if statuses[i] == http.StatusCreated {
			created201++
		} else if statuses[i] != http.StatusOK {
			t.Errorf("response %d status = %d, want 200 or 201", i, statuses[i])
		}
	}
	if created201 != 1 {
		t.Errorf("got %d responses with 201, want exactly 1", created201)
	}
}

// Extra: unsupported method on a known path returns 405 method_not_allowed.
func TestMethodNotAllowed(t *testing.T) {
	ts := newTestServer(t)
	var raw map[string]json.RawMessage
	if s := doJSON(t, http.MethodDelete, ts.URL+"/payments", "", "", &raw); s != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", s)
	}
	if c := errCode(t, raw); c != codeMethodNotOK {
		t.Errorf("code = %q, want %q", c, codeMethodNotOK)
	}
}

// Extra: healthz returns 200 {"status":"ok"}.
func TestHealthz(t *testing.T) {
	ts := newTestServer(t)
	var out map[string]string
	if s := doJSON(t, http.MethodGet, ts.URL+"/healthz", "", "", &out); s != http.StatusOK {
		t.Fatalf("status = %d, want 200", s)
	}
	if out["status"] != "ok" {
		t.Errorf("body = %v, want status ok", out)
	}
}
