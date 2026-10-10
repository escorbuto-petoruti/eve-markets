package esi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSearchStructures(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/characters/42/search/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("categories") != "structure" || q.Get("search") != "keepstar" || q.Get("strict") != "false" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"structure":[1000000000001,1000000000002]}`))
	})
	got, err := c.SearchStructures(context.Background(), testToken, 42, "keepstar")
	if err != nil || len(got) != 2 || got[0] != 1000000000001 || got[1] != 1000000000002 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSearchStructuresAbsentKeyIsEmpty(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	got, err := c.SearchStructures(context.Background(), testToken, 42, "nothing")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSearchStructuresRejectsShortQuery(t *testing.T) {
	c := newTestClient(t, true, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	for _, q := range []string{"", "ab", "  a "} {
		if _, err := c.SearchStructures(context.Background(), testToken, 42, q); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("query %q err = %v, want ErrInvalidInput", q, err)
		}
	}
	if _, err := c.SearchStructures(context.Background(), testToken, 0, "abcd"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("character 0 err = %v", err)
	}
}

func TestStructure(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/universe/structures/1000000000001/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"Home - Keepstar","owner_id":98000001,"solar_system_id":30000142,"type_id":35834,"position":{"x":1,"y":2,"z":3}}`))
	})
	got, err := c.Structure(context.Background(), testToken, 1000000000001)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Home - Keepstar" || got.OwnerID != 98000001 || got.SolarSystemID != 30000142 || got.TypeID != 35834 {
		t.Errorf("info = %+v", got)
	}
}

func TestStructureForbiddenAndNotFound(t *testing.T) {
	for status, check := range map[int]func(error) bool{403: IsForbidden, 404: IsNotFound} {
		c := newTestClient(t, true, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		})
		if _, err := c.Structure(context.Background(), testToken, 5); !check(err) {
			t.Errorf("status %d: err = %v", status, err)
		}
		if _, _, err := c.StructureOrders(context.Background(), testToken, 5); !check(err) {
			t.Errorf("orders status %d: err = %v", status, err)
		}
	}
	c := newTestClient(t, true, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	if _, err := c.Structure(context.Background(), testToken, 0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("id 0 err = %v", err)
	}
}

func TestStructureOrdersPaginates(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/markets/structures/1000000000001/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("X-Pages", "2")
		switch r.URL.Query().Get("page") {
		case "1", "":
			_, _ = w.Write([]byte(`[{"order_id":1,"type_id":34,"location_id":1000000000001,"is_buy_order":true,"price":5.5,"volume_remain":10,"volume_total":20,"min_volume":1,"range":"station","duration":90,"issued":"2026-10-01T10:00:00Z"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"order_id":2,"type_id":35,"location_id":1000000000001,"is_buy_order":false,"price":6,"volume_remain":1,"volume_total":1,"min_volume":1,"range":"region","duration":30,"issued":"2026-10-02T10:00:00Z"}]`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})
	got, truncated, err := c.StructureOrders(context.Background(), testToken, 1000000000001)
	if err != nil || truncated || len(got) != 2 || got[0].OrderID != 1 || !got[0].IsBuyOrder || got[1].TypeID != 35 {
		t.Fatalf("got %+v truncated=%v err=%v", got, truncated, err)
	}
}

func TestStructureOrdersTruncatedAtCap(t *testing.T) {
	var calls int
	c := newTestClient(t, true, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("X-Pages", strconv.Itoa(maxMarketPages+5))
		_, _ = w.Write([]byte(`[]`))
	})
	_, truncated, err := c.StructureOrders(context.Background(), testToken, 7)
	if err != nil || !truncated || calls != maxMarketPages {
		t.Fatalf("truncated=%v calls=%d err=%v", truncated, calls, err)
	}
}

func TestProbeStructureMarket(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		want       ProbeResult
		wantStatus int
		wantDetail string
		wantErr    bool
	}{
		{"open", http.StatusOK, `[]`, ProbeYes, 200, "", false},
		{"forbidden", http.StatusForbidden, `{"error":"Market access denied"}`, ProbeNo, 403, "Market access denied", false},
		{"not found", http.StatusNotFound, `{"error":"Structure not found"}`, ProbeNo, 404, "Structure not found", false},
		{"forbidden without body", http.StatusForbidden, ``, ProbeNo, 403, "", false},
		{"forbidden long and control chars", http.StatusForbidden, `{"error":"` + strings.Repeat("x", 300) + `\u0007\n"}`, ProbeNo, 403, strings.Repeat("x", 200), false},
		{"forbidden echoing the token", http.StatusForbidden, `{"error":"bad ` + testToken + ` here"}`, ProbeNo, 403, "bad [redacted] here", false},
		{"server error", http.StatusInternalServerError, `{"error":"boom"}`, "", 0, "", true},
		{"bad gateway", http.StatusBadGateway, ``, "", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/markets/structures/1000000000001/" {
					t.Errorf("path = %s", r.URL.Path)
				}
				if got := r.URL.Query().Get("page"); got != "1" {
					t.Errorf("page = %q, want 1", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.ProbeStructureMarket(context.Background(), testToken, 1000000000001)
			if (err != nil) != tc.wantErr || got.Result != tc.want || got.Status != tc.wantStatus || got.Detail != tc.wantDetail {
				t.Fatalf("got %+v, %v; want %q %d %q, err=%v", got, err, tc.want, tc.wantStatus, tc.wantDetail, tc.wantErr)
			}
		})
	}
}

func TestProbeStructureMarketRateLimit(t *testing.T) {
	for _, status := range []int{420, http.StatusTooManyRequests} {
		c := newTestClient(t, true, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(status)
		})
		got, err := c.ProbeStructureMarket(context.Background(), testToken, 5)
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.Status != status || got != (Probe{}) {
			t.Errorf("status %d: got %+v, %v", status, got, err)
		}
	}
}

func TestProbeStructureMarketRejectsBadID(t *testing.T) {
	c := newTestClient(t, true, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	if _, err := c.ProbeStructureMarket(context.Background(), testToken, 0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}
