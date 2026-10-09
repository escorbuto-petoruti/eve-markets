// Package web serves the local markets page, the EVE SSO sign-in and the JSON API.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

//go:embed static/index.html static/app.js static/style.css
var assets embed.FS

// Deps are the collaborators of the handler.
type Deps struct {
	Store *store.Store
	// SSO signs users in. Without it /auth/login answers 503.
	SSO SSO
	// Now is the clock; it defaults to time.Now.
	Now func() time.Time
	// OnLogin runs after a successful sign-in with the user id. It may be nil.
	OnLogin func(userID int64)
	// OnTokenSaved runs right after a sign-in saved new credentials for a
	// character, before OnLogin (a token cache can drop its entry). It may be nil.
	OnTokenSaved func(characterID int64)
	// AllowedPort is the port of the listener: a Host header may carry it (or no
	// port) next to a loopback name. Any other port is refused.
	AllowedPort string
}

type server struct {
	deps  Deps
	flows *loginFlows
	moves *pendingMoves
}

// tokenSaved tells the owner of the token cache that a sign-in stored new
// credentials for the character.
func (s *server) tokenSaved(characterID int64) {
	if s.deps.OnTokenSaved != nil {
		s.deps.OnTokenSaved(characterID)
	}
}

func (s *server) now() time.Time { return s.deps.Now() }

// New returns the handler for the local web app.
func New(deps Deps) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &server{deps: deps}
	s.flows = newLoginFlows(s.now)
	s.moves = newPendingMoves(s.now)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", s.index)
	mux.HandleFunc("/static/{name}", s.static)
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("GET /auth/add-character", s.addCharacter)
	mux.HandleFunc("GET /auth/confirm-move", s.confirmMove)
	mux.HandleFunc("POST /auth/move-character", s.moveCharacter)
	mux.HandleFunc("POST /auth/cancel-move", s.cancelMove)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("/api/me", requireUser(s.me))
	return s.guard(s.withSession(mux))
}

// postPaths are the only fixed paths that accept a POST.
var postPaths = map[string]bool{
	"/auth/logout":         true,
	"/auth/move-character": true,
	"/auth/cancel-move":    true,
}

// guard sets the hardening headers on every response, refuses a Host that is not
// the local app (DNS rebinding) and allows only GET and HEAD, except for the
// POSTs of /auth/logout, /auth/move-character and /auth/cancel-move.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://images.evetech.net")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !allowedHost(r.Host, s.deps.AllowedPort) {
			writeError(w, http.StatusForbidden, "forbidden host")
			return
		}
		post := r.Method == http.MethodPost && postPaths[r.URL.Path]
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !post {
			h.Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	serveAsset(w, r, "static/index.html", "text/html; charset=utf-8")
}

var staticTypes = map[string]string{
	"app.js":     "text/javascript; charset=utf-8",
	"style.css":  "text/css; charset=utf-8",
	"index.html": "text/html; charset=utf-8",
}

func (s *server) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctype, ok := staticTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveAsset(w, r, "static/"+name, ctype)
}

func serveAsset(w http.ResponseWriter, r *http.Request, name, ctype string) {
	b, err := fs.ReadFile(assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	// Embedded assets carry no validators, so ask the browser to refetch them
	// instead of reusing a heuristically cached copy of an older build.
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, r *http.Request, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // escapes <, > and & by default
	if err := enc.Encode(v); err != nil {
		code, buf = http.StatusInternalServerError, *bytes.NewBufferString(`{"error":"internal error"}` + "\n")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(code)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, nil, code, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
