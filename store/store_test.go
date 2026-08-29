package store

import (
	"sync"
	"testing"

	"paymentapi/apperr"
)

// TestStoreConcurrentSameKey exercises the store directly under -race: many
// goroutines create with one key and body, and exactly one payment must exist.
func TestStoreConcurrentSameKey(t *testing.T) {
	s := New()
	const n = 128

	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]struct{}{}
	created := 0
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, isNew, err := s.CreatePayment("same-key", 100, "RUB")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			mu.Lock()
			ids[p.ID] = struct{}{}
			if isNew {
				created++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if len(ids) != 1 {
		t.Fatalf("distinct ids = %d, want 1", len(ids))
	}
	if created != 1 {
		t.Fatalf("created count = %d, want 1", created)
	}
}

// TestStoreReuseDifferentBody confirms the typed reuse error is returned.
func TestStoreReuseDifferentBody(t *testing.T) {
	s := New()
	if _, _, err := s.CreatePayment("k", 100, "RUB"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, _, err := s.CreatePayment("k", 200, "RUB")
	if err != apperr.ErrIdempotencyKeyReuse {
		t.Fatalf("err = %v, want ErrIdempotencyKeyReuse", err)
	}
}
