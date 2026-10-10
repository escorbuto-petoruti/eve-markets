package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/auth"
	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

const (
	scopeStructureMarkets = "esi-markets.structure_markets.v1"
	scopeSearchStructures = "esi-search.search_structures.v1"
	scopeReadStructures   = "esi-universe.read_structures.v1"

	// maxResolvedStructures caps how many search hits are resolved to names.
	maxResolvedStructures = 20
	// structureLookupWorkers bounds the concurrent universe/structures calls.
	structureLookupWorkers = 4
	// structureProbeWorkers bounds the concurrent market-access probes.
	structureProbeWorkers = 4
	// accessTTL is how long a yes/no market-access answer is trusted.
	accessTTL = 24 * time.Hour

	accessYes      = "yes"
	accessNoMarket = "no_market"
	accessUnknown  = "unknown"
)

// Structures reads private structure markets. *esi.Client implements it.
type Structures interface {
	SearchStructures(ctx context.Context, token string, characterID int64, query string) ([]int64, error)
	Structure(ctx context.Context, token string, structureID int64) (esi.StructureInfo, error)
	StructureOrders(ctx context.Context, token string, structureID int64) ([]esi.Order, bool, error)
	ProbeStructureMarket(ctx context.Context, token string, structureID int64) (esi.Probe, error)
	UniverseNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// characterToken validates character_id, checks that the signed-in user owns
// it, that its token has every needed scope and returns an access token. On
// failure it has already answered and ok is false. A character of another user
// and an unknown one are indistinguishable (404).
func (s *server) characterToken(w http.ResponseWriter, r *http.Request, u store.User, scopes ...string) (charID int64, token string, ok bool) {
	if s.deps.Structures == nil || s.deps.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "structure markets unavailable")
		return 0, "", false
	}
	id, valid := positiveID(r.URL.Query().Get("character_id"))
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid character id")
		return 0, "", false
	}
	chars, err := s.deps.Store.CharactersForUser(r.Context(), u.UserID)
	if err != nil {
		slog.Error("list characters", "err", err)
		serverError(w, err)
		return 0, "", false
	}
	if !slices.ContainsFunc(chars, func(c store.UserCharacter) bool { return c.CharacterID == id }) {
		writeError(w, http.StatusNotFound, "character not found")
		return 0, "", false
	}
	tok, found, err := s.deps.Store.GetToken(r.Context(), id)
	if err != nil {
		slog.Error("read token scopes", "character_id", id, "err", err)
		serverError(w, err)
		return 0, "", false
	}
	if !found {
		writeError(w, http.StatusNotFound, "character not found")
		return 0, "", false
	}
	for _, need := range scopes {
		if !slices.Contains(tok.Scopes, need) {
			writeError(w, http.StatusForbidden, "this character lacks the "+need+" scope; re-add the character to grant it")
			return 0, "", false
		}
	}
	access, err := s.deps.Tokens.Token(r.Context(), id)
	if err != nil {
		if errors.Is(err, auth.ErrReauthRequired) {
			writeError(w, http.StatusUnauthorized, "this character's session expired; sign in with that character again")
			return 0, "", false
		}
		if errors.Is(err, context.Canceled) {
			return 0, "", false
		}
		slog.Error("character token", "character_id", id, "err", err)
		writeError(w, http.StatusBadGateway, "could not get a token for this character")
		return 0, "", false
	}
	return id, access, true
}

func positiveID(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil && n > 0
}

// structureError answers an upstream failure of a structure call. ESI does not
// document what its 403 means for a structure market; most likely the structure
// has no market service, so the message does not claim a docking problem.
func structureError(w http.ResponseWriter, r *http.Request, err error) {
	if esi.IsForbidden(err) {
		writeError(w, http.StatusForbidden, "ESI refused the market read for this structure (it likely has no market service, or this character has no market access)")
		return
	}
	upstreamError(w, r, err)
}

type structureHit struct {
	ID   int64   `json:"id"`
	Name *string `json:"name"`
	// MarketAccess is yes, no_market (ESI refused the market read, most likely
	// no market service) or unknown (probe failed or was skipped).
	MarketAccess string `json:"market_access"`
	// MarketStatus and MarketDetail are what ESI answered to the probe (HTTP
	// status, error text); 0 and "" when unknown or cached before they were kept.
	MarketStatus int    `json:"market_status"`
	MarketDetail string `json:"market_detail"`
}

// structureSearch serves GET /api/structures/search?character_id=&q=.
func (s *server) structureSearch(w http.ResponseWriter, r *http.Request, u store.User) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if s.deps.Structures != nil && s.deps.Tokens != nil && len([]rune(q)) < 3 {
		// Checked before the character so a bad query never costs a token refresh.
		writeError(w, http.StatusBadRequest, "q must have at least 3 characters")
		return
	}
	charID, token, ok := s.characterToken(w, r, u, scopeSearchStructures, scopeReadStructures)
	if !ok {
		return
	}
	ids, err := s.deps.Structures.SearchStructures(r.Context(), token, charID, q)
	if err != nil {
		structureError(w, r, err)
		return
	}
	if len(ids) > maxResolvedStructures {
		ids = ids[:maxResolvedStructures]
	}
	hits := make([]structureHit, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, structureLookupWorkers)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			hits[i].ID = id
			info, err := s.deps.Structures.Structure(r.Context(), token, id)
			switch {
			case err == nil:
				name := info.Name
				hits[i].Name = &name
			case esi.IsForbidden(err) || esi.IsNotFound(err):
				// Inaccessible: listed without a name.
			default:
				errs[i] = err
			}
		}()
	}
	wg.Wait()
	if err := firstError(errs); err != nil {
		structureError(w, r, err)
		return
	}
	retryAfter := s.probeAccess(r.Context(), token, charID, hits, r.URL.Query().Get("refresh") == "1")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Round(time.Second)/time.Second)))
	}
	rank := func(h structureHit) int {
		switch h.MarketAccess {
		case accessYes:
			return 0
		case accessUnknown:
			return 1
		}
		return 2 // no_market
	}
	slices.SortStableFunc(hits, func(a, b structureHit) int { return rank(a) - rank(b) })
	writeJSON(w, r, http.StatusOK, hits)
}

// probeAccess fills MarketAccess of every hit: from the cache while younger
// than accessTTL (unless refresh), otherwise by probing with bounded
// concurrency. Definitive answers are cached; transient failures are not and
// leave the hit "unknown". After the first rate limit no new probe starts. It
// returns the longest Retry-After seen, zero when none.
func (s *server) probeAccess(ctx context.Context, token string, charID int64, hits []structureHit, refresh bool) time.Duration {
	now := s.now()
	var pending []int
	for i := range hits {
		hits[i].MarketAccess = accessUnknown
		if !refresh {
			cached, found, err := s.deps.Store.GetStructureAccessDetail(ctx, charID, hits[i].ID)
			if err != nil {
				slog.Error("read structure access cache", "character_id", charID, "structure_id", hits[i].ID, "err", err)
			} else if found && now.Sub(cached.CheckedAt) < accessTTL {
				hits[i].MarketAccess = accessNoMarket
				if cached.Accessible {
					hits[i].MarketAccess = accessYes
				}
				hits[i].MarketStatus = cached.HTTPStatus
				hits[i].MarketDetail = cached.Detail
				continue
			}
		}
		pending = append(pending, i)
	}
	var (
		limited atomic.Bool
		mu      sync.Mutex
		wait    time.Duration
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, structureProbeWorkers)
	for _, i := range pending {
		sem <- struct{}{}
		if limited.Load() || ctx.Err() != nil {
			<-sem
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			id := hits[i].ID
			res, err := s.deps.Structures.ProbeStructureMarket(ctx, token, id)
			if err != nil {
				var rl *esi.RateLimitError
				if errors.As(err, &rl) {
					mu.Lock()
					wait = max(wait, rl.RetryAfter)
					mu.Unlock()
					limited.Store(true)
				}
				if !errors.Is(err, context.Canceled) {
					slog.Warn("probe structure market", "structure_id", id, "err", err)
				}
				return
			}
			yes := res.Result == esi.ProbeYes
			hits[i].MarketAccess = accessNoMarket
			if yes {
				hits[i].MarketAccess = accessYes
			}
			hits[i].MarketStatus = res.Status
			hits[i].MarketDetail = res.Detail
			rec := store.StructureAccess{Accessible: yes, CheckedAt: now, HTTPStatus: res.Status, Detail: res.Detail}
			if err := s.deps.Store.SaveStructureAccessDetail(ctx, charID, id, rec); err != nil {
				slog.Error("cache structure access", "character_id", charID, "structure_id", id, "err", err)
			}
		}()
	}
	wg.Wait()
	return wait
}

func firstError(errs []error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

type structureOrder struct {
	esi.Order
	TypeName string `json:"type_name,omitempty"`
}

// structureOrders serves GET /api/structures/{id}/orders. type_id and
// order_type (buy, sell or all) filter server-side because ESI cannot.
func (s *server) structureOrders(w http.ResponseWriter, r *http.Request, u store.User) {
	structureID, ok := positiveID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid structure id")
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
	_, token, ok := s.characterToken(w, r, u, scopeStructureMarkets)
	if !ok {
		return
	}
	all, truncated, err := s.deps.Structures.StructureOrders(r.Context(), token, structureID)
	if err != nil {
		structureError(w, r, err)
		return
	}
	orders := make([]structureOrder, 0, len(all))
	var typeIDs []int64
	seen := map[int]bool{}
	for _, o := range all {
		if typeID > 0 && o.TypeID != typeID {
			continue
		}
		if (kind == "buy" && !o.IsBuyOrder) || (kind == "sell" && o.IsBuyOrder) {
			continue
		}
		orders = append(orders, structureOrder{Order: o})
		if !seen[o.TypeID] {
			seen[o.TypeID] = true
			typeIDs = append(typeIDs, int64(o.TypeID))
		}
	}
	if len(typeIDs) > 0 {
		names, err := s.deps.Structures.UniverseNames(r.Context(), typeIDs)
		if err != nil {
			// Names are decoration: log and serve the orders without them.
			slog.Warn("resolve type names", "err", err)
		}
		for i := range orders {
			orders[i].TypeName = names[int64(orders[i].TypeID)]
		}
	}
	slices.SortStableFunc(orders, func(a, b structureOrder) int {
		switch {
		case a.IsBuyOrder != b.IsBuyOrder:
			if a.IsBuyOrder {
				return 1
			}
			return -1
		case a.Price == b.Price:
			return 0
		case a.IsBuyOrder == (a.Price > b.Price):
			return -1
		default:
			return 1
		}
	})
	writeJSON(w, r, http.StatusOK, struct {
		Truncated bool             `json:"truncated"`
		Orders    []structureOrder `json:"orders"`
	}{truncated, orders})
}
