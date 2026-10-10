package esi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestRegionOrdersPaginatesAndSendsQuery(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/markets/10000002/orders/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("order_type") != "sell" || q.Get("type_id") != "34" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("X-Pages", "2")
		switch q.Get("page") {
		case "1", "":
			_, _ = w.Write([]byte(`[{"order_id":1,"type_id":34,"location_id":60003760,"is_buy_order":false,"price":5.5,"volume_remain":10,"volume_total":20,"min_volume":1,"range":"station","duration":90,"issued":"2026-10-01T10:00:00Z"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"order_id":2,"type_id":34,"location_id":60003760,"is_buy_order":false,"price":6,"volume_remain":1,"volume_total":1,"min_volume":1,"range":"region","duration":30,"issued":"2026-10-02T10:00:00Z"}]`))
		default:
			t.Errorf("unexpected page %q", q.Get("page"))
		}
	})
	got, err := c.RegionOrders(context.Background(), 10000002, "sell", 34)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || calls.Load() != 2 {
		t.Fatalf("orders = %d, calls = %d, want 2 and 2", len(got), calls.Load())
	}
	o := got[0]
	if o.OrderID != 1 || o.TypeID != 34 || o.LocationID != 60003760 || o.IsBuyOrder || o.Price != 5.5 ||
		o.VolumeRemain != 10 || o.VolumeTotal != 20 || o.MinVolume != 1 || o.Range != "station" ||
		o.Duration != 90 || o.Issued != "2026-10-01T10:00:00Z" {
		t.Errorf("order = %+v", o)
	}
}

func TestRegionOrdersOmitsTypeIDWhenZero(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("type_id") {
			t.Errorf("type_id must be omitted: %v", r.URL.Query())
		}
		if r.URL.Query().Get("order_type") != "all" {
			t.Errorf("query = %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`[]`))
	})
	got, err := c.RegionOrders(context.Background(), 10000002, "all", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestRegionOrdersCapsPages(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Pages", strconv.Itoa(100000))
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.RegionOrders(context.Background(), 10000002, "buy", 34); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != maxMarketPages {
		t.Errorf("calls = %d, want the cap %d", calls.Load(), maxMarketPages)
	}
}

func TestRegionOrdersValidatesBeforeRequesting(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected, got %s", r.URL)
	})
	for _, tc := range []struct {
		region int
		kind   string
		typeID int
	}{
		{0, "sell", 34}, {-1, "sell", 34}, {10000002, "bogus", 34}, {10000002, "", 34}, {10000002, "sell", -5},
	} {
		if _, err := c.RegionOrders(context.Background(), tc.region, tc.kind, tc.typeID); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("RegionOrders(%+v) err = %v, want ErrInvalidInput", tc, err)
		}
	}
}

func TestRegionOrdersErrors(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	var rl *RateLimitError
	if _, err := c.RegionOrders(context.Background(), 10000002, "sell", 34); !errors.As(err, &rl) {
		t.Errorf("err = %v, want RateLimitError", err)
	}
	c = newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	if _, err := c.RegionOrders(context.Background(), 10000002, "sell", 34); err == nil {
		t.Error("want a decode error")
	}
}

func TestRegionHistory(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/markets/10000002/history/" || r.URL.Query().Get("type_id") != "34" {
			t.Errorf("url = %s", r.URL)
		}
		_, _ = w.Write([]byte(`[{"date":"2026-10-01","average":5.1,"highest":6,"lowest":4,"order_count":120,"volume":9000000}]`))
	})
	got, err := c.RegionHistory(context.Background(), 10000002, 34)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	h := got[0]
	if h.Date != "2026-10-01" || h.Average != 5.1 || h.Highest != 6 || h.Lowest != 4 || h.OrderCount != 120 || h.Volume != 9000000 {
		t.Errorf("history = %+v", h)
	}
}

func TestRegionHistoryValidatesBeforeRequesting(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected, got %s", r.URL)
	})
	for _, tc := range [][2]int{{0, 34}, {10000002, 0}, {10000002, -1}} {
		if _, err := c.RegionHistory(context.Background(), tc[0], tc[1]); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("RegionHistory(%v) err = %v, want ErrInvalidInput", tc, err)
		}
	}
}
