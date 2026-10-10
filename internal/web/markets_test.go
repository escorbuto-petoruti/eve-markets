package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
)

type fakeMarkets struct {
	orders    []esi.Order
	history   []esi.HistoryDay
	err       error
	gotRegion int
	gotKind   string
	gotType   int
	calls     int
}

func (f *fakeMarkets) RegionOrders(_ context.Context, region int, kind string, typeID int) ([]esi.Order, error) {
	f.calls++
	f.gotRegion, f.gotKind, f.gotType = region, kind, typeID
	return f.orders, f.err
}

func (f *fakeMarkets) RegionHistory(_ context.Context, region, typeID int) ([]esi.HistoryDay, error) {
	f.calls++
	f.gotRegion, f.gotType = region, typeID
	return f.history, f.err
}

func marketsFixture(t *testing.T, m *fakeMarkets) *fixture {
	t.Helper()
	f := newFixture(t)
	f.anon = New(Deps{Store: f.st, SSO: f.sso, Now: f.clock, Markets: m})
	return f
}

func TestMarketsRequireSignIn(t *testing.T) {
	m := &fakeMarkets{}
	f := marketsFixture(t, m)
	for _, p := range []string{"/api/markets/regions/10000002/orders?type_id=34", "/api/markets/regions/10000002/history?type_id=34"} {
		if rec := do(f.anon, http.MethodGet, p); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous status = %d, want 401", p, rec.Code)
		}
	}
	if m.calls != 0 {
		t.Errorf("upstream called %d times for anonymous users", m.calls)
	}
}

func TestMarketsOrders(t *testing.T) {
	m := &fakeMarkets{orders: []esi.Order{{OrderID: 9, TypeID: 34, Price: 5.5, VolumeRemain: 3, Range: "region"}}}
	f := marketsFixture(t, m)
	rec := do(f.h, http.MethodGet, "/api/markets/regions/10000002/orders?type_id=34&order_type=sell")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got []map[string]any
	decode(t, rec, &got)
	if len(got) != 1 || got[0]["order_id"] != float64(9) || got[0]["price"] != 5.5 {
		t.Errorf("body = %v", got)
	}
	if m.gotRegion != 10000002 || m.gotKind != "sell" || m.gotType != 34 {
		t.Errorf("upstream got %d %q %d", m.gotRegion, m.gotKind, m.gotType)
	}
}

func TestMarketsOrdersEmptyIsAnArrayAndKindDefaultsToAll(t *testing.T) {
	m := &fakeMarkets{}
	f := marketsFixture(t, m)
	rec := do(f.h, http.MethodGet, "/api/markets/regions/10000002/orders?type_id=34")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("status = %d body = %q, want 200 []", rec.Code, rec.Body)
	}
	if m.gotKind != "all" {
		t.Errorf("order type = %q, want all", m.gotKind)
	}
}

func TestMarketsHistory(t *testing.T) {
	m := &fakeMarkets{history: []esi.HistoryDay{{Date: "2026-10-01", Average: 5, Volume: 10}}}
	f := marketsFixture(t, m)
	rec := do(f.h, http.MethodGet, "/api/markets/regions/10000002/history?type_id=34")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got []map[string]any
	decode(t, rec, &got)
	if len(got) != 1 || got[0]["date"] != "2026-10-01" {
		t.Errorf("body = %v", got)
	}
	if m.gotRegion != 10000002 || m.gotType != 34 {
		t.Errorf("upstream got %d %d", m.gotRegion, m.gotType)
	}
}

func TestMarketsBadInput(t *testing.T) {
	m := &fakeMarkets{}
	f := marketsFixture(t, m)
	for _, p := range []string{
		"/api/markets/regions/abc/orders?type_id=34",
		"/api/markets/regions/0/orders?type_id=34",
		"/api/markets/regions/-3/history?type_id=34",
		"/api/markets/regions/10000002/orders?type_id=x",
		"/api/markets/regions/10000002/orders?type_id=-1",
		"/api/markets/regions/10000002/orders?type_id=34&order_type=bogus",
		"/api/markets/regions/10000002/history",
		"/api/markets/regions/10000002/history?type_id=0",
		"/api/markets/regions/10000002/history?type_id=abc",
	} {
		if rec := do(f.h, http.MethodGet, p); rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", p, rec.Code)
		}
	}
	if m.calls != 0 {
		t.Errorf("upstream called %d times on bad input", m.calls)
	}
}

func TestMarketsUpstreamErrors(t *testing.T) {
	m := &fakeMarkets{err: &esi.RateLimitError{Status: 429, RetryAfter: 12 * time.Second}}
	f := marketsFixture(t, m)
	rec := do(f.h, http.MethodGet, "/api/markets/regions/10000002/orders?type_id=34")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "12" {
		t.Errorf("rate limit: status = %d Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
	}

	m.err = &esi.APIError{Status: 500, Message: "secret-token-leak internal detail"}
	rec = do(f.h, http.MethodGet, "/api/markets/regions/10000002/history?type_id=34")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("upstream error status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-token-leak") || strings.Contains(rec.Body.String(), "500") {
		t.Errorf("body leaks upstream detail: %q", rec.Body)
	}

	m.err = errors.New("dial tcp: boom")
	rec = do(f.h, http.MethodGet, "/api/markets/regions/10000002/orders?type_id=34")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("transport error: status = %d body = %q", rec.Code, rec.Body)
	}
}

func TestMarketsUnavailableWithoutClient(t *testing.T) {
	f := newFixture(t)
	if rec := do(f.h, http.MethodGet, "/api/markets/regions/10000002/orders?type_id=34"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
