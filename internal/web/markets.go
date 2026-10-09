package web

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

// Markets reads public market data from ESI. *esi.Client implements it.
type Markets interface {
	RegionOrders(ctx context.Context, regionID int, orderType string, typeID int) ([]esi.Order, error)
	RegionHistory(ctx context.Context, regionID, typeID int) ([]esi.HistoryDay, error)
}

// positiveInt parses a strictly positive int.
func positiveInt(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil && n > 0
}

// regionOrders serves GET /api/markets/regions/{region}/orders. type_id is
// optional (every type); order_type is buy, sell or all (default all).
func (s *server) regionOrders(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.deps.Markets == nil {
		writeError(w, http.StatusServiceUnavailable, "markets unavailable")
		return
	}
	region, ok := positiveInt(r.PathValue("region"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid region id")
		return
	}
	typeID := 0
	if v := r.URL.Query().Get("type_id"); v != "" {
		if typeID, ok = positiveInt(v); !ok {
			writeError(w, http.StatusBadRequest, "invalid type id")
			return
		}
	}
	kind := r.URL.Query().Get("order_type")
	if kind == "" {
		kind = "all"
	}
	if kind != "buy" && kind != "sell" && kind != "all" {
		writeError(w, http.StatusBadRequest, "order_type must be buy, sell or all")
		return
	}
	orders, err := s.deps.Markets.RegionOrders(r.Context(), region, kind, typeID)
	if err != nil {
		upstreamError(w, r, err)
		return
	}
	if orders == nil {
		orders = []esi.Order{}
	}
	writeJSON(w, r, http.StatusOK, orders)
}

// regionHistory serves GET /api/markets/regions/{region}/history; type_id is
// required.
func (s *server) regionHistory(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.deps.Markets == nil {
		writeError(w, http.StatusServiceUnavailable, "markets unavailable")
		return
	}
	region, ok := positiveInt(r.PathValue("region"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid region id")
		return
	}
	typeID, ok := positiveInt(r.URL.Query().Get("type_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "type_id is required and must be positive")
		return
	}
	days, err := s.deps.Markets.RegionHistory(r.Context(), region, typeID)
	if err != nil {
		upstreamError(w, r, err)
		return
	}
	if days == nil {
		days = []esi.HistoryDay{}
	}
	writeJSON(w, r, http.StatusOK, days)
}

// upstreamError maps an ESI failure to a generic response: 429 with
// Retry-After for rate limits, 400 for rejected input, 502 otherwise. The
// upstream message is never forwarded to the client, only logged.
func upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	var rl *esi.RateLimitError
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.As(err, &rl):
		slog.Warn("esi rate limited", "path", r.URL.Path, "err", err)
		if rl.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(rl.RetryAfter.Seconds()))))
		}
		writeError(w, http.StatusTooManyRequests, "ESI is rate limiting requests; try again later")
	case errors.Is(err, esi.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid request")
	default:
		slog.Error("esi request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusBadGateway, "ESI request failed")
	}
}
