package web

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
)

const (
	defaultJitaLimit = 20
	maxJitaLimit     = 50
	jitaConcurrency  = 4
	quoteTTL         = 5 * time.Minute
)

// hub is a public trading hub: the prices of one station inside a region.
type hub struct {
	Name      string
	RegionID  int
	StationID int64
}

// jitaHub is the only hub for now: Jita IV - Moon 4 - Caldari Navy Assembly
// Plant, in The Forge. Further hubs are added to this table.
var jitaHub = hub{Name: "jita", RegionID: 10000002, StationID: 60003760}

// hubQuote is the best price on each side at a hub; 0 means no order.
type hubQuote struct {
	BestSell float64
	BestBuy  float64
}

// bestPrices reduces region orders to the best sell (lowest) and best buy
// (highest) at one station. Orders of other stations are ignored.
func bestPrices(orders []esi.Order, station int64) hubQuote {
	var q hubQuote
	for _, o := range orders {
		if o.LocationID != station {
			continue
		}
		if o.IsBuyOrder {
			if o.Price > q.BestBuy {
				q.BestBuy = o.Price
			}
		} else if q.BestSell == 0 || o.Price < q.BestSell {
			q.BestSell = o.Price
		}
	}
	return q
}

// compare returns alliance - hub and that difference as a percentage of the
// hub price. Both are nil when either side has no price.
func compare(alliance, hubPrice float64) (diff, pct *float64) {
	if alliance <= 0 || hubPrice <= 0 {
		return nil, nil
	}
	d := alliance - hubPrice
	p := d / hubPrice * 100
	return &d, &p
}

type quoteKey struct {
	hub    string
	typeID int64
}

type quoteEntry struct {
	quote   hubQuote
	expires time.Time
}

// quoteCache is a TTL cache of hub quotes. The caller supplies the time, so
// the cache follows the injected clock. It is safe for concurrent use.
type quoteCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[quoteKey]quoteEntry
}

func newQuoteCache(ttl time.Duration) *quoteCache {
	return &quoteCache{ttl: ttl, m: map[quoteKey]quoteEntry{}}
}

func (c *quoteCache) get(k quoteKey, now time.Time) (hubQuote, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || !now.Before(e.expires) {
		delete(c.m, k)
		return hubQuote{}, false
	}
	return e.quote, true
}

func (c *quoteCache) put(k quoteKey, q hubQuote, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = quoteEntry{quote: q, expires: now.Add(c.ttl)}
}

// fetchQuotes returns the hub quote of every type it could get, reading the
// cache first and calling ESI (at most jitaConcurrency at a time) for the rest.
// failed counts the types left out because ESI failed; errors are not cached.
func fetchQuotes(ctx context.Context, m Markets, c *quoteCache, h hub, typeIDs []int64, now time.Time) (quotes map[int64]hubQuote, failed int) {
	quotes = make(map[int64]hubQuote, len(typeIDs))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, jitaConcurrency)
	)
	for _, id := range typeIDs {
		key := quoteKey{hub: h.Name, typeID: id}
		if q, ok := c.get(key, now); ok {
			quotes[id] = q
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			orders, err := m.RegionOrders(ctx, h.RegionID, "all", int(id))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				slog.Warn("hub orders", "hub", h.Name, "type_id", id, "err", err)
				failed++
				return
			}
			q := bestPrices(orders, h.StationID)
			c.put(key, q, now)
			quotes[id] = q
		}()
	}
	wg.Wait()
	return quotes, failed
}

// jitaJSON is the hub side of a demand row.
type jitaJSON struct {
	Hub      string   `json:"hub"`
	BestSell float64  `json:"best_sell"`
	BestBuy  float64  `json:"best_buy"`
	SellDiff *float64 `json:"sell_diff"`
	SellPct  *float64 `json:"sell_pct"`
	BuyDiff  *float64 `json:"buy_diff"`
	BuyPct   *float64 `json:"buy_pct"`
}

// jitaMetaJSON reports how the comparison went. Status is ok, partial (some
// types failed) or unavailable (no client, or every type failed).
type jitaMetaJSON struct {
	Hub       string `json:"hub"`
	Status    string `json:"status"`
	Requested int    `json:"requested"`
	Failed    int    `json:"failed"`
}

// jitaCaches holds one quote cache per server (there is one in production);
// it keeps the cache out of the server struct.
var jitaCaches sync.Map // *server -> *quoteCache

func (s *server) quoteCache() *quoteCache {
	c, _ := jitaCaches.LoadOrStore(s, newQuoteCache(quoteTTL))
	return c.(*quoteCache)
}

// attachJita fills the jita field of the first limit rows and reports the
// outcome. Rows beyond the limit, or whose lookup failed, keep jita nil.
func (s *server) attachJita(ctx context.Context, rows []demandRowJSON, limit int) *jitaMetaJSON {
	h := jitaHub
	n := min(limit, len(rows))
	meta := &jitaMetaJSON{Hub: h.Name, Status: "ok", Requested: n}
	if n == 0 {
		return meta
	}
	if s.deps.Markets == nil {
		meta.Status, meta.Failed = "unavailable", n
		return meta
	}
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = rows[i].TypeID
	}
	quotes, failed := fetchQuotes(ctx, s.deps.Markets, s.quoteCache(), h, ids, s.now())
	meta.Failed = failed
	switch {
	case failed == n:
		meta.Status = "unavailable"
	case failed > 0:
		meta.Status = "partial"
	}
	for i := 0; i < n; i++ {
		q, ok := quotes[rows[i].TypeID]
		if !ok {
			continue
		}
		j := &jitaJSON{Hub: h.Name, BestSell: q.BestSell, BestBuy: q.BestBuy}
		j.SellDiff, j.SellPct = compare(rows[i].BestSell, q.BestSell)
		j.BuyDiff, j.BuyPct = compare(rows[i].BestBuy, q.BestBuy)
		rows[i].Jita = j
	}
	return meta
}
