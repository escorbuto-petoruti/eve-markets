package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

type demandBody struct {
	StructureID int64  `json:"structure_id"`
	Name        string `json:"name"`
	Meta        struct {
		FirstCollectedAt *int64 `json:"first_collected_at"`
		LastRunAt        *int64 `json:"last_run_at"`
		Since            int64  `json:"since"`
		Until            int64  `json:"until"`
		TotalFillEvents  int64  `json:"total_fill_events"`
	} `json:"meta"`
	Rows []struct {
		TypeID         int64   `json:"type_id"`
		Name           string  `json:"name"`
		SoldUnits      int64   `json:"sold_units"`
		SoldValue      float64 `json:"sold_value"`
		BoughtUnits    int64   `json:"bought_units"`
		BoughtValue    float64 `json:"bought_value"`
		FillEvents     int64   `json:"fill_events"`
		OpenBuyVolume  int64   `json:"open_buy_volume"`
		OpenBuyValue   float64 `json:"open_buy_value"`
		OpenSellVolume int64   `json:"open_sell_volume"`
		BestSell       float64 `json:"best_sell"`
		BestBuy        float64 `json:"best_buy"`
		LastFillAt     *int64  `json:"last_fill_at"`
	} `json:"rows"`
}

// demandFixture watches structure 500 with Alice's character and stores two
// fills (one 2 days old, one 10 days old) and some open orders.
func demandFixture(t *testing.T, s *fakeStructures) *fixture {
	t.Helper()
	f := structuresFixture(t, s, &fakeTokens{}, allScopes()...)
	ctx := context.Background()
	now := f.clock()
	if err := f.st.AddWatch(ctx, 1, 500, "Keepstar", now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordRun(ctx, 1, 500, now.Add(-time.Minute), "ok", ""); err != nil {
		t.Fatal(err)
	}
	orders := []store.StoredOrder{
		{OrderID: 1, TypeID: 34, IsBuy: true, Price: 4, VolumeRemain: 1000, VolumeTotal: 1000, Issued: "x", Duration: 90, FirstSeen: now.Add(-20 * 24 * time.Hour), LastSeen: now},
		{OrderID: 2, TypeID: 35, IsBuy: false, Price: 9, VolumeRemain: 10, VolumeTotal: 10, Issued: "x", Duration: 90, FirstSeen: now.Add(-20 * 24 * time.Hour), LastSeen: now},
	}
	events := []store.OrderEvent{
		{OrderID: 2, TypeID: 35, At: now.Add(-48 * time.Hour), Kind: store.EventFill, Price: 9, DeltaVolume: 5},
		{OrderID: 2, TypeID: 35, At: now.Add(-10 * 24 * time.Hour), Kind: store.EventFill, Price: 9, DeltaVolume: 100},
	}
	if err := f.st.ApplyOrderDiff(ctx, 500, now, orders, nil, events); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDemandRequiresSignIn(t *testing.T) {
	f := demandFixture(t, &fakeStructures{})
	if rec := do(f.anon, http.MethodGet, "/api/demand?structure_id=500"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDemandRowsAndNames(t *testing.T) {
	f := demandFixture(t, &fakeStructures{names: map[int64]string{34: "Tritanium", 35: "Pyerite"}})
	rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got demandBody
	decode(t, rec, &got)
	if got.StructureID != 500 || got.Name != "Keepstar" {
		t.Errorf("header = %+v", got)
	}
	now := f.clock()
	// Default window is 7 days: the 10-day-old fill is outside.
	if got.Meta.Until != now.Unix() || got.Meta.Since != now.Add(-7*24*time.Hour).Unix() || got.Meta.TotalFillEvents != 1 {
		t.Errorf("meta = %+v", got.Meta)
	}
	if got.Meta.LastRunAt == nil || *got.Meta.LastRunAt != now.Add(-time.Minute).Unix() || got.Meta.FirstCollectedAt == nil {
		t.Errorf("meta coverage = %+v", got.Meta)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %+v", got.Rows)
	}
	r := got.Rows[0]
	if r.TypeID != 35 || r.Name != "Pyerite" || r.SoldUnits != 5 || r.SoldValue != 45 || r.FillEvents != 1 ||
		r.OpenSellVolume != 10 || r.BestSell != 9 || r.LastFillAt == nil || *r.LastFillAt != now.Add(-48*time.Hour).Unix() {
		t.Errorf("row 0 = %+v", r)
	}
	r = got.Rows[1]
	if r.TypeID != 34 || r.Name != "Tritanium" || r.OpenBuyVolume != 1000 || r.OpenBuyValue != 4000 || r.BestBuy != 4 || r.LastFillAt != nil {
		t.Errorf("row 1 = %+v", r)
	}
	// A wider window sees the old fill; sort and limit are honored.
	decode(t, do(f.h, http.MethodGet, "/api/demand?structure_id=500&days=30&sort=open_buy_value&limit=1"), &got)
	if got.Meta.TotalFillEvents != 2 || len(got.Rows) != 1 || got.Rows[0].TypeID != 34 {
		t.Errorf("30d/open_buy_value/limit1 = %+v", got)
	}
}

func TestDemandNamesUnavailableStillServes(t *testing.T) {
	f := demandFixture(t, &fakeStructures{namesErr: errors.New("esi down")})
	rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got demandBody
	decode(t, rec, &got)
	if len(got.Rows) != 2 || got.Rows[0].Name != "" || got.Rows[0].TypeID != 35 {
		t.Errorf("rows = %+v", got.Rows)
	}
}

func TestDemandWithoutStructuresClientStillServes(t *testing.T) {
	f := demandFixture(t, &fakeStructures{})
	f.anon = New(Deps{Store: f.st, SSO: f.sso, Now: f.clock})
	if rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500"); rec.Code != http.StatusOK {
		t.Errorf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestDemandEmptyStructure(t *testing.T) {
	f := structuresFixture(t, &fakeStructures{}, &fakeTokens{}, allScopes()...)
	_ = f.st.AddWatch(context.Background(), 1, 700, "New", f.clock())
	rec := do(f.h, http.MethodGet, "/api/demand?structure_id=700")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got demandBody
	decode(t, rec, &got)
	if got.Rows == nil || len(got.Rows) != 0 || got.Meta.FirstCollectedAt != nil || got.Meta.LastRunAt != nil {
		t.Errorf("empty = %+v", got)
	}
}

func TestDemandNotWatchedIs404(t *testing.T) {
	f := demandFixture(t, &fakeStructures{})
	// Bob's watch (another user) and an unknown structure look the same.
	_ = f.st.AddWatch(context.Background(), 2, 600, "bobs", f.clock())
	for _, id := range []string{"600", "12345"} {
		rec := do(f.h, http.MethodGet, "/api/demand?structure_id="+id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("structure %s status = %d, want 404", id, rec.Code)
		}
		if rec.Body.String() != `{"error":"structure not found"}`+"\n" {
			t.Errorf("structure %s body = %q", id, rec.Body)
		}
	}
}

func TestDemandValidation(t *testing.T) {
	f := demandFixture(t, &fakeStructures{})
	for _, q := range []string{
		"", "structure_id=0", "structure_id=abc", "structure_id=-1",
		"structure_id=500&days=0", "structure_id=500&days=91", "structure_id=500&days=x",
		"structure_id=500&limit=0", "structure_id=500&limit=501", "structure_id=500&limit=x",
		"structure_id=500&sort=price", "structure_id=500&sort=",
	} {
		if rec := do(f.h, http.MethodGet, "/api/demand?"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%q status = %d, want 400", q, rec.Code)
		}
	}
	for _, q := range []string{"days=1", "days=90", "limit=1", "limit=500", "sort=sold_units", "sort=bought_value"} {
		if rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500&"+q); rec.Code != http.StatusOK {
			t.Errorf("%q status = %d, want 200: %s", q, rec.Code, rec.Body)
		}
	}
	if rec := do(f.h, http.MethodPost, "/api/demand?structure_id=500"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// breakTable renames a table behind the store's back so that queries on it fail
// while the rest of the database (sessions, tokens) keeps working.
func breakTable(t *testing.T, f *fixture, table string) {
	t.Helper()
	raw, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`ALTER TABLE ` + table + ` RENAME TO broken_` + table); err != nil {
		t.Fatal(err)
	}
}

func TestDemandStoreFailuresAre500(t *testing.T) {
	for _, table := range []string{"watched_structures", "order_events"} {
		t.Run(table, func(t *testing.T) {
			f := demandFixture(t, &fakeStructures{})
			breakTable(t, f, table)
			rec := do(f.h, http.MethodGet, "/api/demand?structure_id=500")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
			}
			if rec.Body.String() != `{"error":"internal error"}`+"\n" {
				t.Errorf("body = %q, want the generic message without store details", rec.Body)
			}
		})
	}
}
