package web

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

const (
	// maxWatchBody bounds the JSON body of the watch endpoints.
	maxWatchBody = 4 << 10
	// maxWatchName bounds the stored structure name, in runes.
	maxWatchName = 200
)

type watchRequest struct {
	CharacterID int64  `json:"character_id"`
	StructureID int64  `json:"structure_id"`
	Name        string `json:"name"`
}

// readWatchRequest enforces same-origin, decodes the size-limited JSON body
// and checks that the character belongs to the signed-in user. On failure it
// has already answered and ok is false.
func (s *server) readWatchRequest(w http.ResponseWriter, r *http.Request, u store.User) (req watchRequest, ok bool) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-site request refused")
		return req, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWatchBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return req, false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return req, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return req, false
	}
	if req.CharacterID <= 0 || req.StructureID <= 0 {
		writeError(w, http.StatusBadRequest, "character_id and structure_id must be positive")
		return req, false
	}
	chars, err := s.deps.Store.CharactersForUser(r.Context(), u.UserID)
	if err != nil {
		slog.Error("list characters", "err", err)
		serverError(w, err)
		return req, false
	}
	if !slices.ContainsFunc(chars, func(c store.UserCharacter) bool { return c.CharacterID == req.CharacterID }) {
		writeError(w, http.StatusNotFound, "character not found")
		return req, false
	}
	return req, true
}

// watchStructure serves POST /api/watch {character_id, structure_id, name?}.
func (s *server) watchStructure(w http.ResponseWriter, r *http.Request, u store.User) {
	req, ok := s.readWatchRequest(w, r, u)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(name) > maxWatchName {
		name = string([]rune(name)[:maxWatchName])
	}
	if err := s.deps.Store.AddWatch(r.Context(), req.CharacterID, req.StructureID, name, s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "character not found")
			return
		}
		slog.Error("watch structure", "character_id", req.CharacterID, "structure_id", req.StructureID, "err", err)
		serverError(w, err)
		return
	}
	slog.Info("watch structure", "character_id", req.CharacterID, "structure_id", req.StructureID)
	writeJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
}

// unwatchStructure serves POST /api/unwatch {character_id, structure_id}.
func (s *server) unwatchStructure(w http.ResponseWriter, r *http.Request, u store.User) {
	req, ok := s.readWatchRequest(w, r, u)
	if !ok {
		return
	}
	if err := s.deps.Store.RemoveWatch(r.Context(), req.CharacterID, req.StructureID); err != nil {
		slog.Error("unwatch structure", "character_id", req.CharacterID, "structure_id", req.StructureID, "err", err)
		serverError(w, err)
		return
	}
	slog.Info("unwatch structure", "character_id", req.CharacterID, "structure_id", req.StructureID)
	writeJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
}

type watchedJSON struct {
	CharacterID int64  `json:"character_id"`
	StructureID int64  `json:"structure_id"`
	Name        string `json:"name"`
	// LastRunAt is unix seconds, null before the first collection.
	LastRunAt  *int64 `json:"last_run_at"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error"`
	OrderCount int    `json:"order_count"`
}

// watchedList serves GET /api/watched: the watchlist of the user's characters.
func (s *server) watchedList(w http.ResponseWriter, r *http.Request, u store.User) {
	list, err := s.deps.Store.ListWatchedForUser(r.Context(), u.UserID)
	if err != nil {
		slog.Error("list watched structures", "err", err)
		serverError(w, err)
		return
	}
	out := make([]watchedJSON, 0, len(list))
	for _, x := range list {
		j := watchedJSON{CharacterID: x.CharacterID, StructureID: x.StructureID, Name: x.Name,
			LastStatus: x.LastStatus, LastError: x.LastError, OrderCount: x.OrderCount}
		if !x.LastRunAt.IsZero() {
			ts := x.LastRunAt.Unix()
			j.LastRunAt = &ts
		}
		out = append(out, j)
	}
	writeJSON(w, r, http.StatusOK, out)
}
