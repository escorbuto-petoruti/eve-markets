package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

var base = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// fixture serves the app over a real SQLite file. f.h is the handler seen by a
// browser signed in as Alice (user 1) when the request carries no cookie of
// its own; f.anon is the bare handler.
type fixture struct {
	h, anon     http.Handler
	st          *store.Store
	dbPath      string
	sso         *fakeSSO
	now         atomic.Int64 // unix seconds
	logins      []int64
	forgot      []forgotten
	aliceCookie string
	mu          sync.Mutex
}

func (f *fixture) clock() time.Time { return time.Unix(f.now.Load(), 0).UTC() }

// forgotten records an OnTokenSaved call and the refresh token stored at that
// moment: the hook must run after the save.
type forgotten struct {
	id      int64
	refresh string
}

func (f *fixture) forgotten() []forgotten {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forgotten(nil), f.forgot...)
}

func (f *fixture) onTokenSaved(id int64) {
	tok, _, _ := f.st.GetToken(context.Background(), id)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, forgotten{id, tok.RefreshToken})
}

// wantForgotten fails unless exactly one cached token was dropped, for id,
// after the new refresh token was stored.
func (f *fixture) wantForgotten(t *testing.T, id int64, refresh string) {
	t.Helper()
	if got := f.forgotten(); len(got) != 1 || got[0].id != id || got[0].refresh != refresh {
		t.Errorf("OnTokenSaved calls = %+v, want one for %d after storing %q", got, id, refresh)
	}
}

func (f *fixture) loggedIn() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.logins...)
}

// newSession stores a session of userID and returns the cookie value.
func (f *fixture) newSession(t *testing.T, userID int64, ttl time.Duration) string {
	t.Helper()
	val := "session-value-" + strconv.FormatInt(userID, 10) + "-" + strconv.FormatInt(int64(ttl), 10)
	if err := f.st.CreateSession(context.Background(), hashSession(val), userID, f.clock(), f.clock().Add(ttl)); err != nil {
		t.Fatal(err)
	}
	return val
}

func (f *fixture) addUser(t *testing.T, id int64, name string) {
	t.Helper()
	if err := f.st.UpsertUser(context.Background(), id, name, f.clock()); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := t.TempDir() + "/w.db"
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, dbPath: path, sso: newFakeSSO()}
	f.now.Store(base.Unix())
	f.anon = New(Deps{Store: st, SSO: f.sso, Now: f.clock, OnTokenSaved: f.onTokenSaved, OnLogin: func(id int64) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logins = append(f.logins, id)
	}})
	f.addUser(t, 1, "Alice")
	f.aliceCookie = f.newSession(t, 1, 7*24*time.Hour)
	f.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}
		f.anon.ServeHTTP(w, r)
	})
	return f
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "localhost"
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder, target string) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; img-src 'self' https://images.evetech.net",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s %s: header %s = %q, want %q", target, strconv.Itoa(rec.Code), k, got, v)
		}
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("%s: unexpected CORS header %q", target, got)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	f := newFixture(t)
	targets := []string{"/", "/static/app.js", "/static/style.css", "/api/me", "/auth/login", "/nope", "/static/", "/static/missing.js"}
	for _, target := range targets {
		assertSecurityHeaders(t, do(f.h, http.MethodGet, target), target)
	}
	assertSecurityHeaders(t, do(f.h, http.MethodPost, "/api/me"), "POST")
}

func TestMethods(t *testing.T) {
	f := newFixture(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		rec := do(f.h, m, "/api/me")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s Allow = %q", m, got)
		}
	}
	rec := do(f.h, http.MethodHead, "/api/me")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD status = %d, body len = %d", rec.Code, rec.Body.Len())
	}
}

func TestIndexAndStatic(t *testing.T) {
	f := newFixture(t)
	tests := []struct{ path, ctype string }{
		{"/", "text/html"},
		{"/static/app.js", "text/javascript"},
		{"/static/format.js", "text/javascript"},
		{"/static/style.css", "text/css"},
	}
	for _, tt := range tests {
		rec := do(f.h, http.MethodGet, tt.path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", tt.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.ctype) {
			t.Errorf("%s content type = %q, want %s", tt.path, ct, tt.ctype)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s empty body", tt.path)
		}
	}
	if body := do(f.h, http.MethodGet, "/").Body.String(); !strings.Contains(body, "Sign in with EVE Online") {
		t.Error("index lacks the sign-in link")
	}
}

func TestNoDirectoryListingOrUnknownPaths(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/static/", "/static", "/static/missing.js", "/nope", "/static/../go.mod", "/index.html"} {
		rec := do(f.h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently &&
			rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("%s status = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "app.js") && !strings.Contains(p, "index") {
			t.Errorf("%s leaks a listing", p)
		}
	}
}

func TestAppFilesHaveNoExternalURLs(t *testing.T) {
	for _, name := range []string{"static/index.html", "static/format.js", "static/app.js", "static/style.css"} {
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		// The only allowed external reference is the EVE image server (img-src).
		s := strings.ReplaceAll(string(b), "https://images.evetech.net/", "")
		if strings.Contains(s, "http://") || strings.Contains(s, "https://") || strings.Contains(s, "//cdn") {
			t.Errorf("%s references an external URL", name)
		}
	}
	idx, _ := fs.ReadFile(assets, "static/index.html")
	if strings.Contains(strings.ToLower(string(idx)), "<script>") || strings.Contains(string(idx), "onclick=") ||
		strings.Contains(string(idx), "style=") {
		t.Error("index.html must not contain inline scripts or styles")
	}
}

func TestAppJSNeverUsesInnerHTML(t *testing.T) {
	b, err := fs.ReadFile(assets, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(string(b), bad) {
			t.Errorf("app.js uses %s", bad)
		}
	}
}

// saveChar registers a character under the user by storing its token.
func (f *fixture) saveChar(t *testing.T, userID, charID int64, name string) {
	t.Helper()
	err := f.st.SaveToken(context.Background(), store.Token{
		CharacterID: charID, UserID: userID, CharacterName: name, RefreshToken: "r-" + name, Scopes: []string{"s"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStaticAssetsAreEmbedded(t *testing.T) {
	for _, name := range []string{"static/index.html", "static/format.js", "static/app.js", "static/style.css"} {
		if _, err := fs.ReadFile(assets, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	f := newFixture(t)
	if rec := do(f.h, http.MethodGet, "/static/chart.umd.min.js"); rec.Code != http.StatusNotFound {
		t.Errorf("removed asset status = %d, want 404", rec.Code)
	}
}

func TestShutdownRouteIsGone(t *testing.T) {
	f := newFixture(t)
	if rec := do(f.h, http.MethodPost, "/api/shutdown"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/shutdown status = %d, want 405", rec.Code)
	}
}

func TestMeListsCharacters(t *testing.T) {
	f := newFixture(t)
	f.saveChar(t, 1, 1, "Alice")
	f.saveChar(t, 1, 30, "Alt")
	var got struct {
		Name       string `json:"name"`
		Characters []struct {
			Name string `json:"name"`
		} `json:"characters"`
	}
	decode(t, do(f.h, http.MethodGet, "/api/me"), &got)
	if got.Name != "Alice" || len(got.Characters) != 2 || got.Characters[1].Name != "Alt" {
		t.Fatalf("me = %+v", got)
	}
}
