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
)

// Structures reads private structure markets. *esi.Client implements it.
type Structures interface {
	SearchStructures(ctx context.Context, token string, characterID int64, query string) ([]int64, error)
	Structure(ctx context.Context, token string, structureID int64) (esi.StructureInfo, error)
	StructureOrders(ctx context.Context, token string, structureID int64) ([]esi.Order, bool, error)
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

// structureError answers an upstream failure of a structure call; an ESI 403
// means the character may not dock or has no market access there.
func structureError(w http.ResponseWriter, r *http.Request, err error) {
	if esi.IsForbidden(err) {
		writeError(w, http.StatusForbidden, "no market access to this structure (docking/ACL)")
		return
	}
	upstreamError(w, r, err)
}

type structureHit struct {
	ID         int64   `json:"id"`
	Name       *string `json:"name"`
	Accessible bool    `json:"accessible"`
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
				hits[i].Name, hits[i].Accessible = &name, true
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
	writeJSON(w, r, http.StatusOK, hits)
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
