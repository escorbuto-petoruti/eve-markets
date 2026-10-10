package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
)

const jitaStation = 60003760

// perTypeMarkets answers RegionOrders per type id, unlike fakeMarkets, which
// returns the same orders for every call.
type perTypeMarkets struct {
	mu       sync.Mutex
	orders   map[int][]esi.Order
	errs     map[int]error
	calls    map[int]int
	regions  []int
	kinds    []string
	delay    time.Duration
	inflight int
	maxSeen  int
}

func (p *perTypeMarkets) RegionOrders(_ context.Context, region int, kind string, typeID int) ([]esi.Order, error) {
	p.mu.Lock()
	if p.calls == nil {
		p.calls = map[int]int{}
	}
	p.calls[typeID]++
	p.regions = append(p.regions, region)
	p.kinds = append(p.kinds, kind)
	p.inflight++
	if p.inflight > p.maxSeen {
		p.maxSeen = p.inflight
	}
	orders, err := p.orders[typeID], p.errs[typeID]
	p.mu.Unlock()
	time.Sleep(p.delay)
	p.mu.Lock()
	p.inflight--
	p.mu.Unlock()
	return orders, err
}

func (p *perTypeMarkets) RegionHistory(context.Context, int, int) ([]esi.HistoryDay, error) {
	return nil, errors.New("not used")
}

func (p *perTypeMarkets) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		n += c
	}
	return n
}

func (p *perTypeMarkets) setErr(typeID int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.errs == nil {
		p.errs = map[int]error{}
	}
	p.errs[typeID] = err
}

func ord(typeID int, loc int64, buy bool, price float64) esi.Order {
	return esi.Order{TypeID: typeID, LocationID: loc, IsBuyOrder: buy, Price: price}
}

func TestBestPricesFiltersStation(t *testing.T) {
	orders := []esi.Order{
		ord(34, jitaStation, false, 8), ord(34, jitaStation, false, 7.5), ord(34, jitaStation, false, 9),
		ord(34, 999, false, 1), // another station: ignored
		ord(34, jitaStation, true, 5), ord(34, jitaStation, true, 4.5),
		ord(34, 999, true, 50), // another station: ignored
	}
	got := bestPrices(orders, jitaStation)
	if got.BestSell != 7.5 || got.BestBuy != 5 {
		t.Errorf("bestPrices = %+v, want sell 7.5 buy 5", got)
	}
	if got := bestPrices(nil, jitaStation); got != (hubQuote{}) {
		t.Errorf("no orders = %+v, want zero", got)
	}
}

func TestCompare(t *testing.T) {
	diff, pct := compare(9, 8)
	if diff == nil || pct == nil || *diff != 1 || *pct != 12.5 {
		t.Errorf("compare(9, 8) = %v, %v", diff, pct)
	}
	diff, pct = compare(6, 8)
	if diff == nil || *diff != -2 || *pct != -25 {
		t.Errorf("compare(6, 8) = %v, %v", diff, pct)
	}
	for _, c := range [][2]float64{{0, 8}, {9, 0}, {0, 0}} {
		if d, p := compare(c[0], c[1]); d != nil || p != nil {
			t.Errorf("compare(%v, %v) = %v, %v, want nil", c[0], c[1], d, p)
		}
	}
}

func TestQuoteCacheTTL(t *testing.T) {
	c := newQuoteCache(5 * time.Minute)
	k := quoteKey{hub: "jita", typeID: 34}
	if _, ok := c.get(k, base); ok {
		t.Fatal("empty cache hit")
	}
	c.put(k, hubQuote{BestSell: 3}, base)
	if q, ok := c.get(k, base.Add(5*time.Minute-time.Second)); !ok || q.BestSell != 3 {
		t.Errorf("fresh entry = %+v, %v", q, ok)
	}
	if _, ok := c.get(k, base.Add(5*time.Minute)); ok {
		t.Error("entry at ttl must be expired")
	}
	if _, ok := c.get(quoteKey{hub: "jita", typeID: 35}, base); ok {
		t.Error("other type hit")
	}
}

func TestFetchQuotesBoundedConcurrencyAndCache(t *testing.T) {
	m := &perTypeMarkets{orders: map[int][]esi.Order{}, delay: 15 * time.Millisecond}
	var ids []int64
	for i := 1; i <= 12; i++ {
		m.orders[i] = []esi.Order{ord(i, jitaStation, false, float64(i))}
		ids = append(ids, int64(i))
	}
	c := newQuoteCache(5 * time.Minute)
	got, failed := fetchQuotes(context.Background(), m, c, jitaHub, ids, base)
	if failed != 0 || len(got) != 12 {
		t.Fatalf("failed = %d, quotes = %d", failed, len(got))
	}
	for _, id := range ids {
		if got[id].BestSell != float64(id) {
			t.Errorf("type %d = %+v", id, got[id])
		}
	}
	if m.maxSeen > 4 || m.maxSeen < 2 {
		t.Errorf("max concurrent calls = %d, want 2..4", m.maxSeen)
	}
	for _, r := range m.regions {
		if r != 10000002 {
			t.Errorf("region = %d, want The Forge", r)
		}
	}
	for _, k := range m.kinds {
		if k != "all" {
			t.Errorf("order type = %q, want all", k)
		}
	}
	// A second pass inside the TTL does not touch ESI.
	before := m.total()
	if _, failed := fetchQuotes(context.Background(), m, c, jitaHub, ids, base.Add(time.Minute)); failed != 0 || m.total() != before {
		t.Errorf("cached pass called ESI: %d -> %d (failed %d)", before, m.total(), failed)
	}
}

func TestFetchQuotesDoesNotCacheErrors(t *testing.T) {
	m := &perTypeMarkets{orders: map[int][]esi.Order{34: {ord(34, jitaStation, false, 5)}}}
	m.setErr(34, errors.New("esi down"))
	c := newQuoteCache(5 * time.Minute)
	got, failed := fetchQuotes(context.Background(), m, c, jitaHub, []int64{34}, base)
	if failed != 1 || len(got) != 0 {
		t.Fatalf("failed = %d, quotes = %v", failed, got)
	}
	m.setErr(34, nil)
	got, failed = fetchQuotes(context.Background(), m, c, jitaHub, []int64{34}, base.Add(time.Second))
	if failed != 0 || got[34].BestSell != 5 {
		t.Errorf("retry = %v, failed %d", got, failed)
	}
}

// jitaFixture serves the demand fixture (type 35 sells at 9 in the alliance,
// type 34 has a buy order at 4) with Jita prices behind a per-type fake.
func jitaFixture(t *testing.T, m Markets) *fixture {
	t.Helper()
	f := demandFixture(t, &fakeStructures{names: map[int64]string{34: "Tritanium", 35: "Pyerite"}})
	f.anon = New(Deps{Store: f.st, SSO: f.sso, Now: f.clock, Structures: &fakeStructures{names: map[int64]string{34: "Tritanium", 35: "Pyerite"}}, Tokens: &fakeTokens{}, Markets: m})
	return f
}

type jitaRow struct {
	TypeID int64 `json:"type_id"`
	Jita   *struct {
		Hub      string   `json:"hub"`
		BestSell float64  `json:"best_sell"`
		BestBuy  float64  `json:"best_buy"`
		SellDiff *float64 `json:"sell_diff"`
		SellPct  *float64 `json:"sell_pct"`
		BuyDiff  *float64 `json:"buy_diff"`
		BuyPct   *float64 `json:"buy_pct"`
	} `json:"jita"`
}

type jitaBody struct {
	Meta struct {
		Jita *struct {
			Hub       string `json:"hub"`
			Status    string `json:"status"`
			Requested int    `json:"requested"`
			Failed    int    `json:"failed"`
		} `json:"jita"`
	} `json:"meta"`
	Rows []jitaRow `json:"rows"`
}

func jitaMarkets() *perTypeMarkets {
	return &perTypeMarkets{orders: map[int][]esi.Order{
		35: {ord(35, jitaStation, false, 8), ord(35, 999, false, 1), ord(35, jitaStation, true, 5)},
		34: {ord(34, jitaStation, true, 3.2), ord(34, 999, true, 90)},
	}}
}

func TestDemandJitaOffByDefault(t *testing.T) {
	m := jitaMarkets()
	f := jitaFixture(t, m)
	var got jitaBody
	decode(t, do(f.h, http.MethodGet, "/api/demand?structure_id=500"), &got)
	if m.total() != 0 || got.Meta.Jita != nil || len(got.Rows) != 2 || got.Rows[0].Jita != nil || got.Rows[1].Jita != nil {
		t.Errorf("calls = %d, body = %+v", m.total(), got)
	}
}

func TestDemandJitaComparison(t *testing.T) {
	m := jitaMarkets()
	f := jitaFixture(t, m)
	rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500&jita=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got jitaBody
	decode(t, rec, &got)
	if got.Meta.Jita == nil || got.Meta.Jita.Status != "ok" || got.Meta.Jita.Hub != "jita" || got.Meta.Jita.Requested != 2 || got.Meta.Jita.Failed != 0 {
		t.Errorf("meta.jita = %+v", got.Meta.Jita)
	}
	// Row 0 is type 35: alliance sell 9 vs Jita 8, alliance has no buy order.
	j := got.Rows[0].Jita
	if got.Rows[0].TypeID != 35 || j == nil || j.BestSell != 8 || j.BestBuy != 5 ||
		j.SellDiff == nil || *j.SellDiff != 1 || *j.SellPct != 12.5 || j.BuyDiff != nil || j.BuyPct != nil {
		t.Errorf("row 0 = %+v (jita %+v)", got.Rows[0], j)
	}
	// Row 1 is type 34: alliance buy 4 vs Jita 3.2, nobody sells at either side.
	j = got.Rows[1].Jita
	if got.Rows[1].TypeID != 34 || j == nil || j.BestSell != 0 || j.BestBuy != 3.2 ||
		j.SellDiff != nil || j.SellPct != nil || j.BuyDiff == nil || *j.BuyPct < 24.99 || *j.BuyPct > 25.01 {
		t.Errorf("row 1 = %+v (jita %+v)", got.Rows[1], j)
	}
}

func TestDemandJitaLimit(t *testing.T) {
	m := jitaMarkets()
	f := jitaFixture(t, m)
	var got jitaBody
	decode(t, do(f.h, http.MethodGet, "/api/demand?structure_id=500&jita=1&jita_limit=1"), &got)
	if m.total() != 1 || got.Rows[0].Jita == nil || got.Rows[1].Jita != nil {
		t.Errorf("calls = %d, rows = %+v", m.total(), got.Rows)
	}
	if got.Meta.Jita == nil || got.Meta.Jita.Requested != 1 || got.Meta.Jita.Status != "ok" {
		t.Errorf("meta.jita = %+v", got.Meta.Jita)
	}
}

func TestDemandJitaDegradation(t *testing.T) {
	m := jitaMarkets()
	m.setErr(34, errors.New("esi down"))
	f := jitaFixture(t, m)
	var got jitaBody
	rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500&jita=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	decode(t, rec, &got)
	if got.Meta.Jita.Status != "partial" || got.Meta.Jita.Failed != 1 || got.Rows[0].Jita == nil || got.Rows[1].Jita != nil {
		t.Errorf("partial = %+v rows %+v", got.Meta.Jita, got.Rows)
	}
	m.setErr(35, errors.New("esi down"))
	f.now.Add(int64(quoteTTL/time.Second) + 1) // type 35 is cached until the TTL passes
	decode(t, do(f.h, http.MethodGet, "/api/demand?structure_id=500&jita=1"), &got)
	if got.Meta.Jita.Status != "unavailable" || got.Rows[0].Jita != nil || got.Rows[1].Jita != nil {
		t.Errorf("unavailable = %+v rows %+v", got.Meta.Jita, got.Rows)
	}
}

func TestDemandJitaWithoutMarketsClient(t *testing.T) {
	f := jitaFixture(t, nil)
	f.anon = New(Deps{Store: f.st, SSO: f.sso, Now: f.clock})
	rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500&jita=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got jitaBody
	decode(t, rec, &got)
	if got.Meta.Jita == nil || got.Meta.Jita.Status != "unavailable" || len(got.Rows) != 2 || got.Rows[0].Jita != nil {
		t.Errorf("body = %+v", got)
	}
}

func TestDemandJitaCacheUsesInjectedClock(t *testing.T) {
	m := jitaMarkets()
	f := jitaFixture(t, m)
	get := func() { do(f.h, http.MethodGet, "/api/demand?structure_id=500&jita=1") }
	get()
	get()
	if m.total() != 2 {
		t.Fatalf("calls inside the TTL = %d, want 2 (one per type)", m.total())
	}
	f.now.Add(int64((5*time.Minute + time.Second) / time.Second))
	get()
	if m.total() != 4 {
		t.Errorf("calls after the TTL = %d, want 4", m.total())
	}
}

func TestDemandJitaValidation(t *testing.T) {
	f := jitaFixture(t, jitaMarkets())
	for _, q := range []string{"jita=2", "jita=x", "jita=", "jita=1&jita_limit=0", "jita=1&jita_limit=51", "jita=1&jita_limit=x"} {
		if rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500&"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%q status = %d, want 400", q, rec.Code)
		}
	}
	for _, q := range []string{"jita=0", "jita=1", "jita=1&jita_limit=50", "jita=1&jita_limit=1"} {
		if rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500&"+q); rec.Code != http.StatusOK {
			t.Errorf("%q status = %d, want 200: %s", q, rec.Code, rec.Body)
		}
	}
}
