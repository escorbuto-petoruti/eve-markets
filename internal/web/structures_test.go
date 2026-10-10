package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/auth"
	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

const (
	scopeMarkets = "esi-markets.structure_markets.v1"
	scopeSearch  = "esi-search.search_structures.v1"
	scopeRead    = "esi-universe.read_structures.v1"
)

type fakeStructures struct {
	mu          sync.Mutex
	searchIDs   []int64
	searchErr   error
	info        map[int64]esi.StructureInfo
	infoErr     map[int64]error
	orders      []esi.Order
	truncated   bool
	ordersErr   error
	names       map[int64]string
	namesErr    error
	gotToken    string
	gotChar     int64
	gotQuery    string
	gotStruct   int64
	calls       int
	inflight    atomic.Int32
	maxInflight atomic.Int32
	infoCalls   atomic.Int32
}

func (f *fakeStructures) note(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotToken = token
}

func (f *fakeStructures) SearchStructures(_ context.Context, token string, char int64, q string) ([]int64, error) {
	f.note(token)
	f.gotChar, f.gotQuery = char, q
	return f.searchIDs, f.searchErr
}

func (f *fakeStructures) Structure(_ context.Context, token string, id int64) (esi.StructureInfo, error) {
	f.note(token)
	f.infoCalls.Add(1)
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	if err := f.infoErr[id]; err != nil {
		return esi.StructureInfo{}, err
	}
	return f.info[id], nil
}

func (f *fakeStructures) StructureOrders(_ context.Context, token string, id int64) ([]esi.Order, bool, error) {
	f.note(token)
	f.gotStruct = id
	return f.orders, f.truncated, f.ordersErr
}

func (f *fakeStructures) UniverseNames(_ context.Context, ids []int64) (map[int64]string, error) {
	return f.names, f.namesErr
}

type fakeTokens struct {
	err error
	got []int64
}

func (f *fakeTokens) Characters(context.Context) ([]auth.Character, error) { return nil, nil }
func (f *fakeTokens) Token(_ context.Context, id int64) (string, error) {
	f.got = append(f.got, id)
	if f.err != nil {
		return "", f.err
	}
	return "access-token-xyz", nil
}

func structuresFixture(t *testing.T, s *fakeStructures, tk *fakeTokens, scopes ...string) *fixture {
	t.Helper()
	f := newFixture(t)
	f.anon = New(Deps{Store: f.st, SSO: f.sso, Now: f.clock, Structures: s, Tokens: tk})
	f.saveToken(t, 1, 1, "Alice", scopes)
	f.addUser(t, 2, "Bob")
	f.saveToken(t, 2, 2, "Bob", []string{scopeMarkets, scopeSearch, scopeRead})
	return f
}

func (f *fixture) saveToken(t *testing.T, char, user int64, name string, scopes []string) {
	t.Helper()
	err := f.st.SaveToken(context.Background(), store.Token{CharacterID: char, UserID: user, CharacterName: name, RefreshToken: "r", Scopes: scopes, UpdatedAt: f.clock()})
	if err != nil {
		t.Fatal(err)
	}
}

func allScopes() []string { return []string{scopeMarkets, scopeSearch, scopeRead} }

func TestStructuresRequireSignIn(t *testing.T) {
	s := &fakeStructures{}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	for _, p := range []string{"/api/structures/search?character_id=1&q=abc", "/api/structures/5/orders?character_id=1"} {
		if rec := do(f.anon, http.MethodGet, p); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", p, rec.Code)
		}
	}
	if s.calls != 0 {
		t.Errorf("upstream called %d times", s.calls)
	}
}

func TestStructuresRejectPost(t *testing.T) {
	f := structuresFixture(t, &fakeStructures{}, &fakeTokens{}, allScopes()...)
	if rec := do(f.h, http.MethodPost, "/api/structures/5/orders?character_id=1"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestStructureSearchResolvesNames(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{101, 102, 103},
		info:      map[int64]esi.StructureInfo{101: {Name: "Alpha"}, 103: {Name: "Gamma"}},
		infoErr:   map[int64]error{102: &esi.APIError{Status: 403}},
	}
	tk := &fakeTokens{}
	f := structuresFixture(t, s, tk, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/search?character_id=1&q=alp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got []map[string]any
	decode(t, rec, &got)
	if len(got) != 3 {
		t.Fatalf("body = %v", got)
	}
	if got[0]["id"] != float64(101) || got[0]["name"] != "Alpha" || got[0]["accessible"] != true {
		t.Errorf("row 0 = %v", got[0])
	}
	if got[1]["id"] != float64(102) || got[1]["name"] != nil || got[1]["accessible"] != false {
		t.Errorf("row 1 = %v", got[1])
	}
	if s.gotChar != 1 || s.gotQuery != "alp" || s.gotToken != "access-token-xyz" {
		t.Errorf("upstream got char=%d q=%q token=%q", s.gotChar, s.gotQuery, s.gotToken)
	}
	if strings.Contains(rec.Body.String(), "access-token-xyz") {
		t.Error("token leaked into the response")
	}
}

func TestStructureSearchCapsAndBoundsConcurrency(t *testing.T) {
	ids := make([]int64, 50)
	info := map[int64]esi.StructureInfo{}
	for i := range ids {
		ids[i] = int64(1000 + i)
		info[ids[i]] = esi.StructureInfo{Name: "S"}
	}
	s := &fakeStructures{searchIDs: ids, info: info}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/search?character_id=1&q=abc")
	var got []map[string]any
	decode(t, rec, &got)
	if len(got) != 20 || s.infoCalls.Load() != 20 {
		t.Errorf("rows = %d, Structure calls = %d, want 20 and 20", len(got), s.infoCalls.Load())
	}
	if m := s.maxInflight.Load(); m > 4 {
		t.Errorf("max concurrent lookups = %d, want <= 4", m)
	}
}

func TestStructureSearchEmptyIsArray(t *testing.T) {
	f := structuresFixture(t, &fakeStructures{}, &fakeTokens{}, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/search?character_id=1&q=abc")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("status = %d body = %q", rec.Code, rec.Body)
	}
}

func TestStructureSearchValidation(t *testing.T) {
	s := &fakeStructures{searchErr: esi.ErrInvalidInput}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	for _, p := range []string{
		"/api/structures/search?q=abc",
		"/api/structures/search?character_id=0&q=abc",
		"/api/structures/search?character_id=x&q=abc",
		"/api/structures/search?character_id=1&q=ab",
		"/api/structures/search?character_id=1",
	} {
		if rec := do(f.h, http.MethodGet, p); rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", p, rec.Code)
		}
	}
	if s.calls != 0 {
		t.Errorf("upstream called %d times", s.calls)
	}
}

func TestStructuresCharacterMustBelongToUser(t *testing.T) {
	s := &fakeStructures{}
	tk := &fakeTokens{}
	f := structuresFixture(t, s, tk, allScopes()...)
	for _, p := range []string{
		"/api/structures/search?character_id=2&q=abc", // Bob's
		"/api/structures/search?character_id=999&q=abc",
		"/api/structures/5/orders?character_id=2",
		"/api/structures/5/orders?character_id=999",
	} {
		a := do(f.h, http.MethodGet, p)
		if a.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", p, a.Code)
		}
	}
	// Existing and non-existing characters must be indistinguishable.
	a := do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=2")
	b := do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=999")
	if a.Body.String() != b.Body.String() {
		t.Errorf("bodies differ: %q vs %q", a.Body, b.Body)
	}
	if s.calls != 0 || len(tk.got) != 0 {
		t.Errorf("upstream calls = %d, token requests = %v", s.calls, tk.got)
	}
}

func TestStructuresReauthRequired(t *testing.T) {
	tk := &fakeTokens{err: &auth.ReauthError{CharacterID: 1, Name: "Alice"}}
	f := structuresFixture(t, &fakeStructures{}, tk, allScopes()...)
	for _, p := range []string{"/api/structures/search?character_id=1&q=abc", "/api/structures/5/orders?character_id=1"} {
		rec := do(f.h, http.MethodGet, p)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign in") {
			t.Errorf("%s status = %d body = %s", p, rec.Code, rec.Body)
		}
	}
}

func TestStructuresMissingScope(t *testing.T) {
	s := &fakeStructures{}
	f := structuresFixture(t, s, &fakeTokens{}, scopeSearch, scopeRead) // no markets scope
	rec := do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=1")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "re-add") {
		t.Errorf("orders status = %d body = %s", rec.Code, rec.Body)
	}
	f.saveToken(t, 1, 1, "Alice", []string{scopeMarkets})
	rec = do(f.h, http.MethodGet, "/api/structures/search?character_id=1&q=abc")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "re-add") {
		t.Errorf("search status = %d body = %s", rec.Code, rec.Body)
	}
	if s.calls != 0 {
		t.Errorf("upstream called %d times", s.calls)
	}
}

func TestStructureOrdersFiltersSortsAndNames(t *testing.T) {
	s := &fakeStructures{
		orders: []esi.Order{
			{OrderID: 1, TypeID: 34, Price: 5, IsBuyOrder: true},
			{OrderID: 2, TypeID: 34, Price: 7, IsBuyOrder: false},
			{OrderID: 3, TypeID: 34, Price: 6, IsBuyOrder: false},
			{OrderID: 4, TypeID: 34, Price: 4, IsBuyOrder: true},
			{OrderID: 5, TypeID: 35, Price: 1, IsBuyOrder: false},
		},
		truncated: true,
		names:     map[int64]string{34: "Tritanium", 35: "Pyerite"},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/1000000000001/orders?character_id=1&type_id=34")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Truncated bool `json:"truncated"`
		Orders    []struct {
			OrderID  int64  `json:"order_id"`
			TypeName string `json:"type_name"`
		} `json:"orders"`
	}
	decode(t, rec, &got)
	var ids []int64
	for _, o := range got.Orders {
		ids = append(ids, o.OrderID)
		if o.TypeName != "Tritanium" {
			t.Errorf("order %d type name = %q", o.OrderID, o.TypeName)
		}
	}
	want := []int64{3, 2, 1, 4} // sell asc, then buy desc
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	if !got.Truncated {
		t.Error("truncated flag lost")
	}
	if s.gotStruct != 1000000000001 {
		t.Errorf("structure id = %d", s.gotStruct)
	}

	rec = do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=1&type_id=34&order_type=buy")
	decode(t, rec, &got)
	if len(got.Orders) != 2 || got.Orders[0].OrderID != 1 {
		t.Errorf("buy orders = %+v", got.Orders)
	}
}

func TestStructureOrdersNamesFailureDegrades(t *testing.T) {
	s := &fakeStructures{orders: []esi.Order{{OrderID: 1, TypeID: 34}}, namesErr: errors.New("boom")}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"order_id":1`) {
		t.Errorf("status = %d body = %s", rec.Code, rec.Body)
	}
}

func TestStructureOrdersEmptyIsArray(t *testing.T) {
	f := structuresFixture(t, &fakeStructures{}, &fakeTokens{}, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"orders":[]`) {
		t.Errorf("status = %d body = %s", rec.Code, rec.Body)
	}
}

func TestStructureOrdersValidation(t *testing.T) {
	s := &fakeStructures{}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	for _, p := range []string{
		"/api/structures/0/orders?character_id=1",
		"/api/structures/abc/orders?character_id=1",
		"/api/structures/5/orders",
		"/api/structures/5/orders?character_id=-1",
		"/api/structures/5/orders?character_id=1&type_id=0",
		"/api/structures/5/orders?character_id=1&order_type=zzz",
	} {
		if rec := do(f.h, http.MethodGet, p); rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", p, rec.Code)
		}
	}
	if s.calls != 0 {
		t.Errorf("upstream called %d times", s.calls)
	}
}

func TestStructureOrdersUpstreamErrors(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		code  int
		match string
	}{
		{"forbidden", &esi.APIError{Status: 403, Message: "secret detail"}, http.StatusForbidden, "no market access to this structure"},
		{"rate limit", &esi.RateLimitError{Status: 429, RetryAfter: 7 * time.Second}, http.StatusTooManyRequests, "rate limiting"},
		{"other", errors.New("dial tcp: secret internal detail"), http.StatusBadGateway, "ESI request failed"},
	}
	for _, c := range cases {
		s := &fakeStructures{ordersErr: c.err}
		f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
		rec := do(f.h, http.MethodGet, "/api/structures/5/orders?character_id=1")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.match) || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: status = %d body = %s", c.name, rec.Code, rec.Body)
		}
		if c.code == http.StatusTooManyRequests && rec.Header().Get("Retry-After") != "7" {
			t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
	}
}
