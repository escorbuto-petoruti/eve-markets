package esi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// minStructureSearch is the shortest search ESI accepts.
const minStructureSearch = 3

// StructureInfo is the public-to-the-docked data of a player structure.
type StructureInfo struct {
	Name          string `json:"name"`
	OwnerID       int64  `json:"owner_id"`
	SolarSystemID int64  `json:"solar_system_id"`
	TypeID        int64  `json:"type_id"`
	Position      *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		Z float64 `json:"z"`
	} `json:"position,omitempty"`
}

// SearchStructures returns the ids of structures whose name matches query for
// the character (scope esi-search.search_structures.v1). The query needs at
// least three characters. No match yields an empty slice.
func (c *Client) SearchStructures(ctx context.Context, token string, characterID int64, query string) ([]int64, error) {
	query = strings.TrimSpace(query)
	if characterID <= 0 {
		return nil, fmt.Errorf("%w: character id must be positive", ErrInvalidInput)
	}
	if len([]rune(query)) < minStructureSearch {
		return nil, fmt.Errorf("%w: search needs at least %d characters", ErrInvalidInput, minStructureSearch)
	}
	q := url.Values{"categories": {"structure"}, "search": {query}, "strict": {"false"}}
	body, _, err := c.get(ctx, token, "/characters/"+strconv.FormatInt(characterID, 10)+"/search/", q)
	if err != nil {
		return nil, err
	}
	var res struct {
		Structure []int64 `json:"structure"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("esi: decode structure search: %w", err)
	}
	if res.Structure == nil {
		return []int64{}, nil
	}
	return res.Structure, nil
}

// Structure returns the info of a structure (scope
// esi-universe.read_structures.v1). A refused read is a 403
// (see IsForbidden).
func (c *Client) Structure(ctx context.Context, token string, structureID int64) (StructureInfo, error) {
	if structureID <= 0 {
		return StructureInfo{}, fmt.Errorf("%w: structure id must be positive", ErrInvalidInput)
	}
	body, _, err := c.get(ctx, token, "/universe/structures/"+strconv.FormatInt(structureID, 10)+"/", nil)
	if err != nil {
		return StructureInfo{}, err
	}
	var info StructureInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return StructureInfo{}, fmt.Errorf("esi: decode structure: %w", err)
	}
	return info, nil
}

// StructureOrders returns every order of a structure market (scope
// esi-markets.structure_markets.v1), following X-Pages up to maxMarketPages;
// truncated reports that the cap stopped the loop. ESI has no type filter
// here, so callers filter. A 403 (see IsForbidden) is ESI refusing the market read.
func (c *Client) StructureOrders(ctx context.Context, token string, structureID int64) ([]Order, bool, error) {
	if structureID <= 0 {
		return nil, false, fmt.Errorf("%w: structure id must be positive", ErrInvalidInput)
	}
	return c.pagedOrders(ctx, token, "/markets/structures/"+strconv.FormatInt(structureID, 10)+"/", nil)
}

// ProbeResult is the outcome of a definitive market-access probe.
type ProbeResult string

// Definitive probe outcomes. Anything else is an error, never cached. ProbeNo
// means ESI refused the market read (403 or 404); ESI does not document which
// of the two it sends or why, so Probe records what it answered.
const (
	ProbeYes ProbeResult = "yes"
	ProbeNo  ProbeResult = "no"
)

// maxProbeDetail is the longest Probe.Detail kept, in characters.
const maxProbeDetail = 200

// Probe is a definitive probe answer with what ESI said: Status is 200 for
// ProbeYes and 403 or 404 for ProbeNo; Detail is the sanitized "error" text of
// ESI's error body (empty for 200 or when the body had none).
type Probe struct {
	Result ProbeResult
	Status int
	Detail string
}

// ProbeStructureMarket asks whether the token's character can read the market
// of a structure by requesting only the first orders page (scope
// esi-markets.structure_markets.v1); the body is ignored. 200 is ProbeYes, 403
// and 404 are ProbeNo. A rate limit returns the *RateLimitError and any other
// failure its error, both meaning "unknown" to the caller (zero Probe).
func (c *Client) ProbeStructureMarket(ctx context.Context, token string, structureID int64) (Probe, error) {
	if structureID <= 0 {
		return Probe{}, fmt.Errorf("%w: structure id must be positive", ErrInvalidInput)
	}
	q := url.Values{"page": {"1"}}
	_, _, err := c.get(ctx, token, "/markets/structures/"+strconv.FormatInt(structureID, 10)+"/", q)
	var apiErr *APIError
	switch {
	case err == nil:
		return Probe{Result: ProbeYes, Status: http.StatusOK}, nil
	case (IsForbidden(err) || IsNotFound(err)) && errors.As(err, &apiErr):
		return Probe{Result: ProbeNo, Status: apiErr.Status, Detail: sanitizeDetail(apiErr.Message)}, nil
	default:
		return Probe{}, err
	}
}

// sanitizeDetail keeps printable text only (other characters are dropped) and
// cuts it to maxProbeDetail characters. The error message it receives has
// already been scrubbed of the access token.
func sanitizeDetail(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		if n == maxProbeDetail {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
