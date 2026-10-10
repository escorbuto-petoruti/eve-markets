package web

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

const (
	defaultDemandDays  = 7
	maxDemandDays      = 90
	defaultDemandLimit = 100
	maxDemandLimit     = store.MaxDemandLimit
)

var demandSorts = map[string]bool{
	store.DemandSortSoldValue: true, store.DemandSortSoldUnits: true,
	store.DemandSortOpenBuyValue: true, store.DemandSortBoughtValue: true,
}

type demandMetaJSON struct {
	// Timestamps are unix seconds; the first two are null when unknown.
	FirstCollectedAt *int64 `json:"first_collected_at"`
	LastRunAt        *int64 `json:"last_run_at"`
	Since            int64  `json:"since"`
	Until            int64  `json:"until"`
	TotalFillEvents  int64  `json:"total_fill_events"`
	// Jita is present only when the request asked for the hub comparison.
	Jita *jitaMetaJSON `json:"jita,omitempty"`
}

type demandRowJSON struct {
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
	// Jita is null unless the request asked for it and this row got data.
	Jita *jitaJSON `json:"jita"`
}

// intParam reads an optional integer query parameter within [lo, hi]; an
// absent one yields def. ok is false for anything else.
func intParam(r *http.Request, name string, def, lo, hi int) (n int, ok bool) {
	q := r.URL.Query()
	if !q.Has(name) {
		return def, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(q.Get(name)))
	return n, err == nil && n >= lo && n <= hi
}

func unixPtr(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.Unix()
	return &v
}

// demandSummary serves GET /api/demand?structure_id=&days=&limit=&sort=: the
// per-type demand of a structure the signed-in user watches, inferred from the
// fill events of the collector.
func (s *server) demandSummary(w http.ResponseWriter, r *http.Request, u store.User) {
	structureID, valid := positiveID(r.URL.Query().Get("structure_id"))
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid structure id")
		return
	}
	days, ok := intParam(r, "days", defaultDemandDays, 1, maxDemandDays)
	if !ok {
		writeError(w, http.StatusBadRequest, "days must be between 1 and 90")
		return
	}
	limit, ok := intParam(r, "limit", defaultDemandLimit, 1, maxDemandLimit)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
		return
	}
	jitaOn, ok := intParam(r, "jita", 0, 0, 1)
	if !ok {
		writeError(w, http.StatusBadRequest, "jita must be 0 or 1")
		return
	}
	jitaLimit, ok := intParam(r, "jita_limit", defaultJitaLimit, 1, maxJitaLimit)
	if !ok {
		writeError(w, http.StatusBadRequest, "jita_limit must be between 1 and 50")
		return
	}
	sortKey := store.DemandSortSoldValue
	if q := r.URL.Query(); q.Has("sort") {
		sortKey = q.Get("sort")
		if !demandSorts[sortKey] {
			writeError(w, http.StatusBadRequest, "sort must be one of sold_value, sold_units, open_buy_value, bought_value")
			return
		}
	}
	watched, err := s.deps.Store.ListWatchedForUser(r.Context(), u.UserID)
	if err != nil {
		slog.Error("list watched structures", "err", err)
		serverError(w, err)
		return
	}
	name, found := "", false
	for _, x := range watched {
		if x.StructureID == structureID {
			found = true
			if name == "" {
				name = x.Name
			}
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "structure not found")
		return
	}
	until := s.now()
	since := until.Add(-time.Duration(days) * 24 * time.Hour)
	rows, meta, err := s.deps.Store.DemandSummarySorted(r.Context(), structureID, since, until, limit, sortKey)
	if err != nil {
		slog.Error("demand summary", "structure_id", structureID, "err", err)
		serverError(w, err)
		return
	}
	out := make([]demandRowJSON, 0, len(rows))
	typeIDs := make([]int64, 0, len(rows))
	for _, x := range rows {
		out = append(out, demandRowJSON{TypeID: x.TypeID, SoldUnits: x.SoldUnits, SoldValue: x.SoldValue,
			BoughtUnits: x.BoughtUnits, BoughtValue: x.BoughtValue, FillEvents: x.FillEvents,
			OpenBuyVolume: x.OpenBuyVolume, OpenBuyValue: x.OpenBuyValue, OpenSellVolume: x.OpenSellVolume,
			BestSell: x.BestSell, BestBuy: x.BestBuy, LastFillAt: unixPtr(x.LastFillAt)})
		typeIDs = append(typeIDs, x.TypeID)
	}
	if len(typeIDs) > 0 && s.deps.Structures != nil {
		names, err := s.deps.Structures.UniverseNames(r.Context(), typeIDs)
		if err != nil {
			// Names are decoration: log and serve the rows without them.
			slog.Warn("resolve type names", "err", err)
		}
		for i := range out {
			out[i].Name = names[out[i].TypeID]
		}
	}
	outMeta := demandMetaJSON{
		FirstCollectedAt: unixPtr(meta.FirstCollectedAt), LastRunAt: unixPtr(meta.LastRunAt),
		Since: since.Unix(), Until: until.Unix(), TotalFillEvents: meta.TotalFillEvents,
	}
	if jitaOn == 1 {
		outMeta.Jita = s.attachJita(r.Context(), out, jitaLimit)
	}
	writeJSON(w, r, http.StatusOK, struct {
		StructureID int64           `json:"structure_id"`
		Name        string          `json:"name"`
		Meta        demandMetaJSON  `json:"meta"`
		Rows        []demandRowJSON `json:"rows"`
	}{structureID, name, outMeta, out})
}
