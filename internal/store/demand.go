package store

import (
	"context"
	"fmt"
	"time"
)

// Sort keys of DemandSummarySorted. Ties fall back to sold value, then open
// buy value, then type id, so the order is deterministic.
const (
	DemandSortSoldValue    = "sold_value"
	DemandSortSoldUnits    = "sold_units"
	DemandSortOpenBuyValue = "open_buy_value"
	DemandSortBoughtValue  = "bought_value"
)

var demandOrderBy = map[string]string{
	DemandSortSoldValue:    "sold_value DESC, open_buy_value DESC, t.type_id",
	DemandSortSoldUnits:    "sold_units DESC, sold_value DESC, open_buy_value DESC, t.type_id",
	DemandSortOpenBuyValue: "open_buy_value DESC, sold_value DESC, t.type_id",
	DemandSortBoughtValue:  "bought_value DESC, sold_value DESC, open_buy_value DESC, t.type_id",
}

// DemandRow is the demand of one item type in a structure over a window.
//
// Fill events on sell orders are units members bought from sell orders (what
// the structure sells: SoldUnits/SoldValue); fill events on buy orders are
// units sold into buy orders (BoughtUnits/BoughtValue). Values are
// delta_volume * event price. The Open* and Best* fields come from the orders
// open at the last collection, whatever the window.
type DemandRow struct {
	TypeID         int64
	SoldUnits      int64
	SoldValue      float64
	BoughtUnits    int64
	BoughtValue    float64
	FillEvents     int64
	OpenBuyVolume  int64
	OpenBuyValue   float64
	OpenSellVolume int64
	// BestSell is the lowest sell price, BestBuy the highest buy price; 0 when
	// the structure has no such open order.
	BestSell float64
	BestBuy  float64
	// LastFillAt is zero when the type had no fill event in the window.
	LastFillAt time.Time
}

// DemandMeta says how much data stands behind a demand summary.
type DemandMeta struct {
	// FirstCollectedAt is the earliest collection timestamp still on record
	// for the structure: the minimum of orders_current.first_seen and the
	// order_events.at. Both are set from the collection time, unlike the
	// watch's created_at, which predates the first collection. It is a lower
	// bound of the baseline (the baseline orders may have vanished since), and
	// zero when nothing was collected.
	FirstCollectedAt time.Time
	// LastRunAt is the latest last_run_at over the watches of the structure.
	LastRunAt       time.Time
	Since, Until    time.Time
	TotalFillEvents int64
}

// DemandSummary is DemandSummarySorted ordered by sold value.
func (s *Store) DemandSummary(ctx context.Context, structureID int64, since, until time.Time, limit int) ([]DemandRow, DemandMeta, error) {
	return s.DemandSummarySorted(ctx, structureID, since, until, limit, DemandSortSoldValue)
}

// DemandSummarySorted aggregates, per type, the fill events with since <= at
// <= until and the currently open orders of a structure, ordered by sort (one
// of the DemandSort constants) and capped at limit rows. Types with open
// orders but no fills in the window are included. Aggregation runs in SQL
// over the existing (structure_id, type_id, at) and (structure_id, type_id)
// indexes; no new index is needed.
func (s *Store) DemandSummarySorted(ctx context.Context, structureID int64, since, until time.Time, limit int, sort string) ([]DemandRow, DemandMeta, error) {
	orderBy, ok := demandOrderBy[sort]
	if !ok {
		return nil, DemandMeta{}, fmt.Errorf("store: demand summary: unknown sort %q", sort)
	}
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH f AS (
			SELECT type_id,
				SUM(CASE WHEN is_buy = 0 THEN delta_volume ELSE 0 END)         AS sold_units,
				SUM(CASE WHEN is_buy = 0 THEN delta_volume * price ELSE 0 END) AS sold_value,
				SUM(CASE WHEN is_buy = 1 THEN delta_volume ELSE 0 END)         AS bought_units,
				SUM(CASE WHEN is_buy = 1 THEN delta_volume * price ELSE 0 END) AS bought_value,
				COUNT(*) AS fill_events, MAX(at) AS last_fill
			FROM order_events
			WHERE structure_id = ?1 AND kind = 'fill' AND at >= ?2 AND at <= ?3
			GROUP BY type_id
		), o AS (
			SELECT type_id,
				SUM(CASE WHEN is_buy = 1 THEN volume_remain ELSE 0 END)         AS open_buy_volume,
				SUM(CASE WHEN is_buy = 1 THEN volume_remain * price ELSE 0 END) AS open_buy_value,
				SUM(CASE WHEN is_buy = 0 THEN volume_remain ELSE 0 END)         AS open_sell_volume,
				MIN(CASE WHEN is_buy = 0 THEN price END)                        AS best_sell,
				MAX(CASE WHEN is_buy = 1 THEN price END)                        AS best_buy
			FROM orders_current WHERE structure_id = ?1
			GROUP BY type_id
		), t AS (
			SELECT type_id FROM f UNION SELECT type_id FROM o
		)
		SELECT t.type_id,
			COALESCE(f.sold_units, 0) AS sold_units, COALESCE(f.sold_value, 0) AS sold_value,
			COALESCE(f.bought_units, 0) AS bought_units, COALESCE(f.bought_value, 0) AS bought_value,
			COALESCE(f.fill_events, 0), COALESCE(o.open_buy_volume, 0),
			COALESCE(o.open_buy_value, 0) AS open_buy_value, COALESCE(o.open_sell_volume, 0),
			COALESCE(o.best_sell, 0), COALESCE(o.best_buy, 0), f.last_fill
		FROM t LEFT JOIN f ON f.type_id = t.type_id LEFT JOIN o ON o.type_id = t.type_id
		ORDER BY `+orderBy+` LIMIT ?4`,
		structureID, since.Unix(), until.Unix(), limit)
	if err != nil {
		return nil, DemandMeta{}, fmt.Errorf("store: demand summary: %w", err)
	}
	defer rows.Close()
	out := []DemandRow{}
	for rows.Next() {
		var (
			r        DemandRow
			lastFill *int64
		)
		if err := rows.Scan(&r.TypeID, &r.SoldUnits, &r.SoldValue, &r.BoughtUnits, &r.BoughtValue, &r.FillEvents,
			&r.OpenBuyVolume, &r.OpenBuyValue, &r.OpenSellVolume, &r.BestSell, &r.BestBuy, &lastFill); err != nil {
			return nil, DemandMeta{}, fmt.Errorf("store: scan demand row: %w", err)
		}
		if lastFill != nil {
			r.LastFillAt = time.Unix(*lastFill, 0)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, DemandMeta{}, fmt.Errorf("store: demand summary: %w", err)
	}
	meta := DemandMeta{Since: since, Until: until}
	var first, lastRun *int64
	err = s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM order_events WHERE structure_id = ?1 AND kind = 'fill' AND at >= ?2 AND at <= ?3),
			(SELECT MIN(v) FROM (
				SELECT MIN(first_seen) AS v FROM orders_current WHERE structure_id = ?1
				UNION ALL SELECT MIN(at) FROM order_events WHERE structure_id = ?1)),
			(SELECT MAX(last_run_at) FROM watched_structures WHERE structure_id = ?1)`,
		structureID, since.Unix(), until.Unix()).Scan(&meta.TotalFillEvents, &first, &lastRun)
	if err != nil {
		return nil, DemandMeta{}, fmt.Errorf("store: demand meta: %w", err)
	}
	if first != nil {
		meta.FirstCollectedAt = time.Unix(*first, 0)
	}
	if lastRun != nil {
		meta.LastRunAt = time.Unix(*lastRun, 0)
	}
	return out, meta, nil
}
