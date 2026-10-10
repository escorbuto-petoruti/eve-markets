package store

import (
	"context"
	"testing"
	"time"
)

func ts(sec int64) time.Time { return time.Unix(sec, 0) }

func fillEv(order, typ int64, buy bool, at int64, price float64, delta int64) OrderEvent {
	return OrderEvent{OrderID: order, TypeID: typ, IsBuy: buy, At: ts(at), Kind: EventFill, Price: price, DeltaVolume: delta}
}

func openOrd(id, typ int64, buy bool, price float64, remain int64, firstSeen int64) StoredOrder {
	return StoredOrder{OrderID: id, TypeID: typ, IsBuy: buy, Price: price, VolumeRemain: remain, VolumeTotal: remain,
		Issued: "2026-01-01T00:00:00Z", Duration: 90, FirstSeen: ts(firstSeen), LastSeen: ts(firstSeen)}
}

// seedDemand builds a structure 500 with hand-built orders and events plus an
// unrelated structure 600 that must never leak into the results.
func seedDemand(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	orders := []StoredOrder{
		openOrd(1, 34, false, 5, 100, 900),
		openOrd(2, 34, false, 4, 50, 950),
		openOrd(3, 34, true, 3, 200, 950),
		openOrd(4, 35, true, 10, 10, 950),
		openOrd(5, 38, true, 20, 100, 950),
	}
	events := []OrderEvent{
		fillEv(1, 34, false, 1999, 5, 1000), // before the window
		fillEv(1, 34, false, 2000, 5, 10),   // window start is inclusive
		fillEv(2, 34, false, 2100, 4, 5),
		fillEv(3, 34, true, 2200, 3, 20), // sold into a buy order
		{OrderID: 9, TypeID: 34, IsBuy: false, At: ts(2300), Kind: EventGone, Price: 5, DeltaVolume: 99},
		{OrderID: 8, TypeID: 34, IsBuy: false, At: ts(2300), Kind: EventNew, Price: 5, DeltaVolume: 77},
		{OrderID: 7, TypeID: 34, IsBuy: false, At: ts(2300), Kind: EventPrice, Price: 5},
		fillEv(6, 36, false, 3000, 7, 3),   // window end is inclusive
		fillEv(6, 36, false, 3001, 7, 100), // after the window
		fillEv(6, 37, false, 1500, 7, 100), // type with fills only outside the window
	}
	if err := s.ApplyOrderDiff(ctx, 500, ts(3000), orders, nil, events); err != nil {
		t.Fatal(err)
	}
	other := []StoredOrder{openOrd(1, 34, false, 1, 1, 100)}
	if err := s.ApplyOrderDiff(ctx, 600, ts(3000), other, nil, []OrderEvent{fillEv(1, 34, false, 2500, 1, 1000)}); err != nil {
		t.Fatal(err)
	}
}

func typeIDs(rows []DemandRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.TypeID)
	}
	return out
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDemandSummaryAggregates(t *testing.T) {
	s := openTemp(t)
	seedDemand(t, s)
	rows, meta, err := s.DemandSummary(context.Background(), 500, ts(2000), ts(3000), 100)
	if err != nil {
		t.Fatal(err)
	}
	// Sold value desc, then open buy value desc; 37 has nothing in the window.
	if want := []int64{34, 36, 38, 35}; !equalIDs(typeIDs(rows), want) {
		t.Fatalf("types = %v, want %v", typeIDs(rows), want)
	}
	r := rows[0]
	if r.SoldUnits != 15 || r.SoldValue != 70 || r.BoughtUnits != 20 || r.BoughtValue != 60 || r.FillEvents != 3 {
		t.Errorf("fills = %+v", r)
	}
	if r.OpenBuyVolume != 200 || r.OpenBuyValue != 600 || r.OpenSellVolume != 150 || r.BestSell != 4 || r.BestBuy != 3 {
		t.Errorf("open orders = %+v", r)
	}
	if !r.LastFillAt.Equal(ts(2200)) {
		t.Errorf("LastFillAt = %v", r.LastFillAt)
	}
	// Fills but no open orders: best prices are 0.
	if r := rows[1]; r.TypeID != 36 || r.SoldUnits != 3 || r.SoldValue != 21 || r.BestSell != 0 || r.BestBuy != 0 || r.OpenBuyVolume != 0 {
		t.Errorf("type 36 = %+v", r)
	}
	// Open orders only: shows demand through open buy volume, no last fill.
	if r := rows[2]; r.TypeID != 38 || r.SoldUnits != 0 || r.FillEvents != 0 || r.OpenBuyValue != 2000 || r.BestBuy != 20 || r.BestSell != 0 || !r.LastFillAt.IsZero() {
		t.Errorf("type 38 = %+v", r)
	}
	if r := rows[3]; r.TypeID != 35 || r.OpenBuyVolume != 10 || r.OpenBuyValue != 100 {
		t.Errorf("type 35 = %+v", r)
	}
	// Window boundaries: 2000 and 3000 are in, 1999 and 3001 are out; only
	// fill events count (gone/new/price are ignored).
	if meta.TotalFillEvents != 4 {
		t.Errorf("TotalFillEvents = %d, want 4", meta.TotalFillEvents)
	}
	if !meta.Since.Equal(ts(2000)) || !meta.Until.Equal(ts(3000)) {
		t.Errorf("meta window = %v..%v", meta.Since, meta.Until)
	}
}

func TestDemandSummaryOtherStructureIsolated(t *testing.T) {
	s := openTemp(t)
	seedDemand(t, s)
	rows, _, err := s.DemandSummary(context.Background(), 600, ts(0), ts(9999), 100)
	if err != nil || len(rows) != 1 || rows[0].SoldUnits != 1000 || rows[0].OpenSellVolume != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestDemandSummarySortAndLimit(t *testing.T) {
	s := openTemp(t)
	seedDemand(t, s)
	ctx := context.Background()
	cases := []struct {
		sort string
		want []int64
	}{
		{DemandSortSoldValue, []int64{34, 36, 38, 35}},
		{DemandSortSoldUnits, []int64{34, 36, 38, 35}},
		{DemandSortOpenBuyValue, []int64{38, 34, 35, 36}},
		{DemandSortBoughtValue, []int64{34, 36, 38, 35}},
	}
	for _, c := range cases {
		rows, _, err := s.DemandSummarySorted(ctx, 500, ts(2000), ts(3000), 100, c.sort)
		if err != nil || !equalIDs(typeIDs(rows), c.want) {
			t.Errorf("sort %s = %v, %v, want %v", c.sort, typeIDs(rows), err, c.want)
		}
	}
	// The cap applies after sorting.
	rows, _, err := s.DemandSummarySorted(ctx, 500, ts(2000), ts(3000), 1, DemandSortOpenBuyValue)
	if err != nil || !equalIDs(typeIDs(rows), []int64{38}) {
		t.Errorf("limit 1 by open buy value = %v, %v", typeIDs(rows), err)
	}
	rows, _, _ = s.DemandSummary(ctx, 500, ts(2000), ts(3000), 2)
	if !equalIDs(typeIDs(rows), []int64{34, 36}) {
		t.Errorf("limit 2 = %v", typeIDs(rows))
	}
	if _, _, err := s.DemandSummarySorted(ctx, 500, ts(2000), ts(3000), 10, "price; DROP TABLE x"); err == nil {
		t.Error("unknown sort accepted")
	}
}

func TestDemandSummaryEmptyStructure(t *testing.T) {
	s := openTemp(t)
	rows, meta, err := s.DemandSummary(context.Background(), 999, ts(0), ts(100), 10)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 {
		t.Errorf("rows = %#v, want empty non-nil", rows)
	}
	if !meta.FirstCollectedAt.IsZero() || !meta.LastRunAt.IsZero() || meta.TotalFillEvents != 0 {
		t.Errorf("meta = %+v", meta)
	}
}

func TestDemandMetaCoverage(t *testing.T) {
	s := openTemp(t)
	seedDemand(t, s)
	ctx := context.Background()
	seedToken(t, s, 1)
	seedToken(t, s, 2)
	for _, c := range []int64{1, 2} {
		if err := s.AddWatch(ctx, c, 500, "K", ts(100)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordRun(ctx, 1, 500, ts(3100), "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRun(ctx, 2, 500, ts(3200), "ok", ""); err != nil {
		t.Fatal(err)
	}
	_, meta, err := s.DemandSummary(ctx, 500, ts(2000), ts(3000), 10)
	if err != nil {
		t.Fatal(err)
	}
	// Earliest collection timestamp present: first_seen 900 of order 1. The
	// watch's created_at (100) is not a collection and must not be used.
	if !meta.FirstCollectedAt.Equal(ts(900)) {
		t.Errorf("FirstCollectedAt = %v, want 900", meta.FirstCollectedAt)
	}
	if !meta.LastRunAt.Equal(ts(3200)) {
		t.Errorf("LastRunAt = %v, want latest run 3200", meta.LastRunAt)
	}
}
