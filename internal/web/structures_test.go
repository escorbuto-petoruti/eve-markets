package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
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

	// probe results by structure id; a missing id probes as "yes".
	probe         map[int64]esi.ProbeResult
	probeFull     map[int64]esi.Probe
	probeErr      map[int64]error
	probeIDs      []int64
	probeInflight atomic.Int32
	probeMax      atomic.Int32
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

func (f *fakeStructures) ProbeStructureMarket(_ context.Context, token string, id int64) (esi.Probe, error) {
	f.note(token)
	f.mu.Lock()
	f.probeIDs = append(f.probeIDs, id)
	f.mu.Unlock()
	n := f.probeInflight.Add(1)
	defer f.probeInflight.Add(-1)
	for {
		m := f.probeMax.Load()
		if n <= m || f.probeMax.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	if err := f.probeErr[id]; err != nil {
		return esi.Probe{}, err
	}
	if p, ok := f.probeFull[id]; ok {
		return p, nil
	}
	if r, ok := f.probe[id]; ok {
		if r == esi.ProbeNo {
			return esi.Probe{Result: r, Status: 403}, nil
		}
		return esi.Probe{Result: r, Status: 200}, nil
	}
	return esi.Probe{Result: esi.ProbeYes, Status: 200}, nil
}

func (f *fakeStructures) probed() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.probeIDs)
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
		probe:     map[int64]esi.ProbeResult{102: esi.ProbeNo, 103: esi.ProbeNo},
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
	for _, row := range got {
		if _, old := row["accessible"]; old {
			t.Errorf("legacy accessible field still present: %v", row)
		}
	}
	// yes first, then no_market, stable within a group: 101 (yes), 102, 103 (no_market).
	if got[0]["id"] != float64(101) || got[0]["name"] != "Alpha" || got[0]["market_access"] != "yes" {
		t.Errorf("row 0 = %v", got[0])
	}
	if got[1]["id"] != float64(102) || got[1]["name"] != nil || got[1]["market_access"] != "no_market" {
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
		{"forbidden", &esi.APIError{Status: 403, Message: "secret detail"}, http.StatusForbidden, "ESI refused the market read"},
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

func searchRows(t *testing.T, f *fixture, target string) []map[string]any {
	t.Helper()
	rec := do(f.h, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d: %s", target, rec.Code, rec.Body)
	}
	var got []map[string]any
	decode(t, rec, &got)
	return got
}

func accessOf(rows []map[string]any) map[int64]string {
	out := map[int64]string{}
	for _, r := range rows {
		out[int64(r["id"].(float64))], _ = r["market_access"].(string)
	}
	return out
}

func TestStructureSearchOrdersByAccess(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{1, 2, 3, 4, 5},
		info:      map[int64]esi.StructureInfo{1: {Name: "a"}, 2: {Name: "b"}, 3: {Name: "c"}, 4: {Name: "d"}, 5: {Name: "e"}},
		probe:     map[int64]esi.ProbeResult{1: esi.ProbeNo, 2: esi.ProbeYes, 4: esi.ProbeNo, 5: esi.ProbeYes},
		probeErr:  map[int64]error{3: &esi.APIError{Status: 500}},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	rows := searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	var order []int64
	for _, r := range rows {
		order = append(order, int64(r["id"].(float64)))
	}
	if want := []int64{2, 5, 3, 1, 4}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v (yes, unknown, no_market; stable)", order, want)
	}
	if got := accessOf(rows); got[3] != "unknown" || got[2] != "yes" || got[1] != "no_market" {
		t.Errorf("access = %v", got)
	}
}

func TestStructureSearchCachesDefinitiveAnswersFor24h(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{1, 2, 3},
		info:      map[int64]esi.StructureInfo{1: {Name: "a"}, 2: {Name: "b"}, 3: {Name: "c"}},
		probe:     map[int64]esi.ProbeResult{2: esi.ProbeNo},
		probeErr:  map[int64]error{3: &esi.APIError{Status: 502}},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	const target = "/api/structures/search?character_id=1&q=abc"
	searchRows(t, f, target)
	if n := len(s.probed()); n != 3 {
		t.Fatalf("first search probed %d, want 3", n)
	}
	// Second search within TTL: only the transient failure (3) is probed again.
	s.probeIDs = nil
	s.probeErr = nil
	f.now.Add(23 * 3600)
	rows := searchRows(t, f, target)
	if got := s.probed(); !slices.Equal(got, []int64{3}) {
		t.Errorf("second search probed %v, want only [3]", got)
	}
	if got := accessOf(rows); got[1] != "yes" || got[2] != "no_market" || got[3] != "yes" {
		t.Errorf("access = %v", got)
	}
	// After the TTL everything is probed again.
	s.probeIDs = nil
	f.now.Add(2 * 3600)
	searchRows(t, f, target)
	// 3 was re-probed (and cached) at 23h, so it is only 2h old here.
	if got := s.probed(); len(got) != 2 || slices.Contains(got, 3) {
		t.Errorf("after TTL probed %v, want [1 2] in any order", got)
	}
}

func TestStructureSearchRefreshBypassesCache(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{1, 2},
		info:      map[int64]esi.StructureInfo{1: {Name: "a"}, 2: {Name: "b"}},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	s.probeIDs = nil
	searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	if n := len(s.probed()); n != 0 {
		t.Fatalf("cached search probed %d, want 0", n)
	}
	s.probe = map[int64]esi.ProbeResult{1: esi.ProbeNo}
	rows := searchRows(t, f, "/api/structures/search?character_id=1&q=abc&refresh=1")
	if n := len(s.probed()); n != 2 {
		t.Errorf("refresh probed %d, want 2", n)
	}
	if got := accessOf(rows); got[1] != "no_market" {
		t.Errorf("refresh did not update access: %v", got)
	}
}

func TestStructureSearchCacheIsPerCharacter(t *testing.T) {
	s := &fakeStructures{searchIDs: []int64{1}, info: map[int64]esi.StructureInfo{1: {Name: "a"}}}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	f.saveToken(t, 3, 1, "Alice Alt", allScopes())
	searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	s.probeIDs = nil
	searchRows(t, f, "/api/structures/search?character_id=3&q=abc")
	if n := len(s.probed()); n != 1 {
		t.Errorf("other character probed %d, want 1 (cache is per character)", n)
	}
}

func TestStructureSearchProbeCapAndConcurrency(t *testing.T) {
	ids := make([]int64, 50)
	info := map[int64]esi.StructureInfo{}
	for i := range ids {
		ids[i] = int64(1000 + i)
		info[ids[i]] = esi.StructureInfo{Name: "S"}
	}
	s := &fakeStructures{searchIDs: ids, info: info}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	if n := len(s.probed()); n != 20 {
		t.Errorf("probes = %d, want 20", n)
	}
	if m := s.probeMax.Load(); m > 4 || m < 2 {
		t.Errorf("max concurrent probes = %d, want 2..4", m)
	}
}

func TestStructureSearchRateLimitStopsProbing(t *testing.T) {
	ids := make([]int64, 12)
	info := map[int64]esi.StructureInfo{}
	for i := range ids {
		ids[i] = int64(1 + i)
		info[ids[i]] = esi.StructureInfo{Name: "S"}
	}
	s := &fakeStructures{
		searchIDs: ids, info: info,
		probeErr: map[int64]error{1: &esi.RateLimitError{Status: 429, RetryAfter: 30 * time.Second}},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	rec := do(f.h, http.MethodGet, "/api/structures/search?character_id=1&q=abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	var rows []map[string]any
	decode(t, rec, &rows)
	if len(rows) != 12 {
		t.Fatalf("rows = %d, want all 12", len(rows))
	}
	if n := len(s.probed()); n > 4 {
		t.Errorf("probes started = %d, want at most one in-flight window (4)", n)
	}
	if got := accessOf(rows); got[1] != "unknown" || got[12] != "unknown" {
		t.Errorf("access = %v, want unknown for the limited and the unstarted", got)
	}
	// Nothing is cached from a rate-limited run.
	s.probeIDs = nil
	s.probeErr = nil
	searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	// The rate-limited and the never-started ids are probed again; the few
	// definitive answers obtained before the limit may stay cached.
	if got := s.probed(); !slices.Contains(got, 1) || !slices.Contains(got, 12) {
		t.Errorf("follow-up probed %v, want ids 1 and 12 (no transient caching)", got)
	}
}

func TestStructureSearchNameErrorStillFailsRequest(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{1},
		infoErr:   map[int64]error{1: &esi.APIError{Status: 500}},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	if rec := do(f.h, http.MethodGet, "/api/structures/search?character_id=1&q=abc"); rec.Code == http.StatusOK {
		t.Errorf("status = %d, want an error", rec.Code)
	}
}

func TestStructureSearchReportsAndCachesESIAnswer(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{1, 2},
		info:      map[int64]esi.StructureInfo{1: {Name: "a"}, 2: {Name: "b"}},
		probeFull: map[int64]esi.Probe{
			1: {Result: esi.ProbeNo, Status: 404, Detail: "Structure not found"},
			2: {Result: esi.ProbeYes, Status: 200},
		},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	const target = "/api/structures/search?character_id=1&q=abc"
	check := func(label string, rows []map[string]any) {
		t.Helper()
		by := map[int64]map[string]any{}
		for _, r := range rows {
			by[int64(r["id"].(float64))] = r
		}
		if by[1]["market_access"] != "no_market" || by[1]["market_status"] != float64(404) || by[1]["market_detail"] != "Structure not found" {
			t.Errorf("%s: no_market row = %v", label, by[1])
		}
		if by[2]["market_access"] != "yes" || by[2]["market_detail"] != "" {
			t.Errorf("%s: yes row = %v", label, by[2])
		}
	}
	check("fresh", searchRows(t, f, target))
	s.probeIDs = nil
	check("cached", searchRows(t, f, target))
	if n := len(s.probed()); n != 0 {
		t.Errorf("cached search probed %d, want 0", n)
	}
}

func TestStructureSearchUnknownHasNoStatus(t *testing.T) {
	s := &fakeStructures{
		searchIDs: []int64{1},
		info:      map[int64]esi.StructureInfo{1: {Name: "a"}},
		probeErr:  map[int64]error{1: &esi.APIError{Status: 502}},
	}
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	rows := searchRows(t, f, "/api/structures/search?character_id=1&q=abc")
	if rows[0]["market_access"] != "unknown" || rows[0]["market_status"] != float64(0) || rows[0]["market_detail"] != "" {
		t.Errorf("row = %v", rows[0])
	}
}
