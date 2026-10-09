package esi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testToken = "secret-access-token-xyz"
	testUA    = "eve-markets-test/1.0"
)

// newTestClient starts a server running h and returns a client pointed at it.
// Every request is first checked for the mandatory headers.
func newTestClient(t *testing.T, wantAuth bool, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Compatibility-Date"); got != "2020-01-01" {
			t.Errorf("X-Compatibility-Date = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != testUA {
			t.Errorf("User-Agent = %q", got)
		}
		auth := r.Header.Get("Authorization")
		if wantAuth && !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Authorization = %q", auth)
		}
		if wantAuth && auth != "Bearer "+testToken && auth != "Bearer tok-a" && auth != "Bearer tok-b" {
			t.Errorf("Authorization = %q", auth)
		}
		if !wantAuth && auth != "" {
			t.Errorf("unexpected Authorization %q on public call", auth)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), UserAgent: testUA})
}

// ping performs an authenticated GET on a throwaway path and returns the body.
func ping(c *Client, token string) (string, error) {
	body, _, err := c.get(context.Background(), token, "/ping", nil)
	return string(body), err
}

func TestGetSendsHeadersAndBody(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("pong"))
	})
	got, err := ping(c, testToken)
	if err != nil || got != "pong" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPublicCallSendsNoAuthorization(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})
	if got, err := ping(c, ""); err != nil || got != "pong" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestForbidden(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Market structure access denied"}`))
	})
	_, err := ping(c, testToken)
	if !IsForbidden(err) {
		t.Fatalf("IsForbidden false for %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || !strings.Contains(apiErr.Message, "access denied") {
		t.Fatalf("err = %#v", err)
	}
	if IsNotFound(err) {
		t.Fatal("IsNotFound true for 403")
	}
}

func TestNotFound(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if _, err := ping(c, ""); !IsNotFound(err) {
		t.Fatalf("IsNotFound false for %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	for _, status := range []int{420, 429} {
		c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "17")
			w.WriteHeader(status)
		})
		_, err := ping(c, testToken)
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if rl.RetryAfter != 17*time.Second {
			t.Fatalf("RetryAfter = %v", rl.RetryAfter)
		}
	}
}

func TestRateLimitWithoutRetryAfter(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	})
	_, err := ping(c, testToken)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 0 {
		t.Fatalf("err = %#v", err)
	}
}

func TestETagReuse(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			if r.Header.Get("If-None-Match") != "" {
				t.Errorf("first request sent If-None-Match")
			}
			w.Header().Set("ETag", `"abc"`)
			_, _ = w.Write([]byte("55.5"))
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `"abc"` {
			t.Errorf("If-None-Match = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	})
	for i := 0; i < 2; i++ {
		got, err := ping(c, testToken)
		if err != nil {
			t.Fatal(err)
		}
		if got != "55.5" {
			t.Fatalf("call %d body = %q", i, got)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestETagKeepsHeadersOn304(t *testing.T) {
	var full atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", `"p"`)
		w.Header().Set("X-Pages", "2")
		_, _ = w.Write([]byte(`[]`))
	})
	for i := 0; i < 2; i++ {
		_, hdr, err := c.get(context.Background(), testToken, "/ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Get("X-Pages") != "2" {
			t.Fatalf("pass %d: X-Pages = %q", i, hdr.Get("X-Pages"))
		}
	}
	if full.Load() != 1 {
		t.Fatalf("full fetches = %d", full.Load())
	}
}

func TestETagCacheIsPerToken(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("If-None-Match leaked across tokens")
		}
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte("1"))
	})
	for _, tok := range []string{"tok-a", "tok-b"} {
		if _, err := ping(c, tok); err != nil {
			t.Fatal(err)
		}
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"bad"`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("error response was cached")
		}
		_, _ = w.Write([]byte("1"))
	})
	if _, err := ping(c, testToken); err == nil {
		t.Fatal("want error")
	}
	if _, err := ping(c, testToken); err != nil {
		t.Fatal(err)
	}
}

func TestContextCancel(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := c.get(ctx, testToken, "/ping", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	statuses := []int{400, 403, 404, 420, 429, 500}
	for _, status := range statuses {
		c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			// A misbehaving server echoing the credential must not leak it.
			_, _ = w.Write([]byte(`{"error":"bad token ` + r.Header.Get("Authorization") + `"}`))
		})
		_, err := ping(c, testToken)
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("status %d: token leaked: %v", status, err)
		}
	}
}

func TestOversizedBody(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("1", maxBodyBytes+10)))
	})
	if _, err := ping(c, testToken); err == nil {
		t.Fatal("want error")
	}
}

func TestUniverseNames(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/universe/names" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `[3,1,2]` {
			t.Errorf("body = %s (duplicates must be dropped, order kept)", body)
		}
		_, _ = w.Write([]byte(`[{"category":"corporation","id":1,"name":"One"},{"category":"corporation","id":2,"name":"Two"}]`))
	})
	got, err := c.UniverseNames(context.Background(), []int64{3, 1, 3, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "One" || got[2] != "Two" {
		t.Fatalf("got %v", got)
	}
}

func TestUniverseNamesBatchesAndSkipsEmpty(t *testing.T) {
	var sizes []int
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		var ids []int64
		if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
			t.Error(err)
		}
		sizes = append(sizes, len(ids))
		out := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]any{"category": "corporation", "id": id, "name": "n"})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	if got, err := c.UniverseNames(context.Background(), nil); err != nil || len(got) != 0 || len(sizes) != 0 {
		t.Fatalf("empty = %v, %v, calls %v", got, err, sizes)
	}
	ids := make([]int64, 2500)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	got, err := c.UniverseNames(context.Background(), ids)
	if err != nil || len(got) != 2500 {
		t.Fatalf("got %d names, %v", len(got), err)
	}
	if len(sizes) != 3 || sizes[0] != 1000 || sizes[1] != 1000 || sizes[2] != 500 {
		t.Fatalf("batch sizes = %v", sizes)
	}
}

func TestUniverseNamesErrors(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Ensure all IDs are valid"}`))
	})
	if _, err := c.UniverseNames(context.Background(), []int64{1}); !IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	c = newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(420) })
	var rl *RateLimitError
	if _, err := c.UniverseNames(context.Background(), []int64{1}); !errors.As(err, &rl) {
		t.Fatalf("err = %v, want rate limit", err)
	}
}
