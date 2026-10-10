package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func watchPost(f *fixture, h http.Handler, target, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func watchFixture(t *testing.T) *fixture {
	return structuresFixture(t, &fakeStructures{}, &fakeTokens{}, allScopes()...)
}

func TestWatchLifecycle(t *testing.T) {
	f := watchFixture(t)
	rec := watchPost(f, f.h, "/api/watch", `{"character_id":1,"structure_id":500,"name":"Keepstar"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("watch status = %d: %s", rec.Code, rec.Body)
	}
	rec = do(f.h, http.MethodGet, "/api/watched")
	if rec.Code != http.StatusOK {
		t.Fatalf("watched status = %d: %s", rec.Code, rec.Body)
	}
	var got []map[string]any
	decode(t, rec, &got)
	if len(got) != 1 || got[0]["structure_id"] != float64(500) || got[0]["character_id"] != float64(1) ||
		got[0]["name"] != "Keepstar" || got[0]["order_count"] != float64(0) {
		t.Fatalf("watched = %v", got)
	}
	for _, k := range []string{"last_run_at", "last_status", "last_error"} {
		if _, ok := got[0][k]; !ok {
			t.Errorf("missing field %s in %v", k, got[0])
		}
	}
	rec = watchPost(f, f.h, "/api/unwatch", `{"character_id":1,"structure_id":500}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unwatch status = %d: %s", rec.Code, rec.Body)
	}
	rec = do(f.h, http.MethodGet, "/api/watched")
	decode(t, rec, &got)
	if len(got) != 0 {
		t.Errorf("after unwatch = %v", got)
	}
	if rec.Body.String() == "null\n" {
		t.Error("empty list must be [] not null")
	}
}

func TestWatchedListsOnlyOwnCharacters(t *testing.T) {
	f := watchFixture(t)
	ctx := context.Background()
	_ = f.st.AddWatch(ctx, 1, 500, "mine", time.Unix(1, 0))
	_ = f.st.AddWatch(ctx, 2, 600, "bobs", time.Unix(1, 0))
	var got []map[string]any
	decode(t, do(f.h, http.MethodGet, "/api/watched"), &got)
	if len(got) != 1 || got[0]["name"] != "mine" {
		t.Errorf("watched = %v", got)
	}
}

func TestWatchRejectsForeignAndUnknownCharacter(t *testing.T) {
	f := watchFixture(t)
	for _, c := range []string{"2", "99"} {
		rec := watchPost(f, f.h, "/api/watch", `{"character_id":`+c+`,"structure_id":500}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("character %s status = %d, want 404", c, rec.Code)
		}
	}
	// Unwatching another user's watch changes nothing.
	_ = f.st.AddWatch(context.Background(), 2, 600, "bobs", time.Unix(1, 0))
	watchPost(f, f.h, "/api/unwatch", `{"character_id":2,"structure_id":600}`)
	if w, _ := f.st.ListWatched(context.Background()); len(w) != 1 {
		t.Errorf("foreign watch removed: %v", w)
	}
}

func TestWatchValidationAndGuards(t *testing.T) {
	f := watchFixture(t)
	bad := map[string]string{
		"not json":      `nope`,
		"zero char":     `{"character_id":0,"structure_id":5}`,
		"zero struct":   `{"character_id":1,"structure_id":0}`,
		"unknown field": `{"character_id":1,"structure_id":5,"x":1}`,
		"trailing":      `{"character_id":1,"structure_id":5} {}`,
		"huge":          `{"character_id":1,"structure_id":5,"name":"` + strings.Repeat("a", 10000) + `"}`,
	}
	for name, body := range bad {
		if rec := watchPost(f, f.h, "/api/watch", body); rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status = %d, want 400/413", name, rec.Code)
		}
	}
	// Cross-site and missing origin evidence are refused.
	rec := watchPost(f, f.h, "/api/watch", `{"character_id":1,"structure_id":5}`, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site status = %d, want 403", rec.Code)
	}
	// Anonymous.
	rec = watchPost(f, f.anon, "/api/watch", `{"character_id":1,"structure_id":5}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}
	if rec := do(f.anon, http.MethodGet, "/api/watched"); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous list status = %d, want 401", rec.Code)
	}
	if rec := do(f.h, http.MethodGet, "/api/watch"); rec.Code == http.StatusOK {
		t.Errorf("GET /api/watch status = %d", rec.Code)
	}
	if w, _ := f.st.ListWatched(context.Background()); len(w) != 0 {
		t.Errorf("rejected requests stored watches: %v", w)
	}
}
