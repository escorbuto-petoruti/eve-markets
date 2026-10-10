package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/auth"
	"github.com/escorbuto-petoruti/eve-markets/internal/sso"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
	"github.com/escorbuto-petoruti/eve-markets/internal/web"
)

// serveEnv is a minimal environment in which serve may start.
func serveEnv() map[string]string {
	return map[string]string{"HOME": "/home/u", "EVE_MARKETS_CLIENT_ID": "test-client"}
}

type fakeTokens struct {
	forgot []int64
	mu     sync.Mutex
}

// Forget records the characters whose cached token was dropped.
func (f *fakeTokens) Forget(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, id)
}

func (f *fakeTokens) forgotten() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.forgot...)
}

func (f *fakeTokens) Characters(context.Context) ([]auth.Character, error) { return nil, nil }

func (f *fakeTokens) Token(context.Context, int64) (string, error) { return "secret-token", nil }

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	deps      deps
	out, err  *syncBuffer
	tokens    *fakeTokens
	dbPath    string
	listeners chan net.Listener
	opened    *store.Store // the store openStore returned
	tokensFor *store.Store // the store newTokens was given
}

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	h := &harness{
		out:       &syncBuffer{},
		err:       &syncBuffer{},
		tokens:    &fakeTokens{},
		listeners: make(chan net.Listener, 1),
	}
	h.deps = deps{
		stdout: h.out,
		stderr: h.err,
		getenv: func(k string) string { return env[k] },
		openStore: func(path string) (*store.Store, error) {
			h.dbPath = path
			st, err := store.Open(":memory:")
			h.opened = st
			return st, err
		},
		newTokens: func(st *store.Store) auth.TokenSource {
			h.tokensFor = st
			return h.tokens
		},
		newSSO: func() web.SSO { return fakeLoginSSO{} },
		listen: func(network, addr string) (net.Listener, error) {
			ln, err := net.Listen(network, addr)
			if err == nil {
				h.listeners <- ln
			}
			return ln, err
		},
	}
	return h
}

// fakeLoginSSO signs in character 1 ("Alice") without any network.
type fakeLoginSSO struct{}

func (fakeLoginSSO) AuthURL(state, challenge string) string {
	return "https://sso.test/authorize?state=" + url.QueryEscape(state) + "&code_challenge=" + url.QueryEscape(challenge)
}

func (fakeLoginSSO) Exchange(context.Context, string, string) (sso.TokenSet, error) {
	return sso.TokenSet{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: time.Minute}, nil
}

func (fakeLoginSSO) Validate(context.Context, string) (sso.Claims, error) {
	return sso.Claims{CharacterID: 1, CharacterName: "Alice", Scopes: sso.MarketScopes()}, nil
}

// signIn runs the whole sign-in flow against a served app and returns a client
// holding the session cookie.
func signIn(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(base + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil {
		t.Fatalf("login status = %d (%v)", resp.StatusCode, err)
	}
	resp, err = c.Get(base + "/auth/callback?code=x&state=" + url.QueryEscape(loc.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}
	return c
}

func TestUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		out  string // expected on stdout
		err  string // expected on stderr
	}{
		{"no arguments", nil, 2, "", "Usage:"},
		{"help command", []string{"help"}, 2, "", "Usage:"},
		{"unknown command", []string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
		{"long help flag", []string{"--help"}, 0, "Usage:", ""},
		{"short help flag", []string{"-h"}, 0, "Usage:", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if got := run(context.Background(), tt.args, h.deps); got != tt.code {
				t.Errorf("exit = %d, want %d", got, tt.code)
			}
			if !strings.Contains(h.out.String(), tt.out) {
				t.Errorf("stdout = %q, want %q", h.out.String(), tt.out)
			}
			if !strings.Contains(h.err.String(), tt.err) {
				t.Errorf("stderr = %q, want %q", h.err.String(), tt.err)
			}
		})
	}
}

// Starting the server by double-click is out of scope: without arguments the
// program prints the help on every platform and never serves.
func TestNoArgumentsNeverServes(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		h := newHarness(t, serveEnv())
		h.deps.goos = goos
		if got := run(context.Background(), nil, h.deps); got != 2 {
			t.Errorf("%s: exit = %d, want 2", goos, got)
		}
		if !strings.Contains(h.err.String(), "Usage:") {
			t.Errorf("%s: stderr = %q", goos, h.err.String())
		}
		select {
		case <-h.listeners:
			t.Errorf("%s: must not listen", goos)
		default:
		}
	}
}

func TestOnlyTheKeptCommandsExist(t *testing.T) {
	for _, cmd := range []string{"collect", "backfill", "wallets", "label"} {
		h := newHarness(t, nil)
		if got := run(context.Background(), []string{cmd}, h.deps); got != 2 {
			t.Errorf("%s: exit = %d, want 2", cmd, got)
		}
		if !strings.Contains(h.err.String(), "unknown command") {
			t.Errorf("%s: stderr = %q", cmd, h.err.String())
		}
	}
	if strings.Contains(usage, "Quit") || strings.Contains(usage, "eve-markets collect") {
		t.Errorf("usage mentions removed features: %s", usage)
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	h := newHarness(t, nil)
	if got := run(context.Background(), []string{"serve", "-h"}, h.deps); got != 0 {
		t.Errorf("serve -h exit = %d, want 0", got)
	}
}

func TestCheckLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:8089", true},
		{"127.0.0.1:0", true},
		{"[::1]:8089", true},
		{"localhost:8089", true},
		{"0.0.0.0:8089", false},
		{"[::]:8089", false},
		{":8089", false},
		{"example.com:8089", false},
		{"192.168.1.10:8089", false},
		{"127.0.0.1", false},
		{"127.0.0.1:notaport", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := checkLoopbackAddr(tt.addr)
			if (err == nil) != tt.ok {
				t.Errorf("checkLoopbackAddr(%q) = %v, want ok=%v", tt.addr, err, tt.ok)
			}
		})
	}
}

func TestServeRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8089", "[::]:8089", "example.com:8089"} {
		h := newHarness(t, serveEnv())
		if got := run(context.Background(), []string{"serve", "--addr", addr}, h.deps); got != 2 {
			t.Errorf("%s: exit = %d, want 2", addr, got)
		}
		if !strings.Contains(h.err.String(), "loopback") {
			t.Errorf("%s: stderr = %q", addr, h.err.String())
		}
		select {
		case <-h.listeners:
			t.Errorf("%s: must not listen", addr)
		default:
		}
		if h.dbPath != "" {
			t.Errorf("%s: store must not be opened", addr)
		}
	}
}

func TestServeStartsWithoutClientIDEnv(t *testing.T) {
	for _, env := range []map[string]string{
		{"HOME": "/home/u"},
		{"HOME": "/home/u", "EVE_MARKETS_CLIENT_ID": "   "},
	} {
		h := newHarness(t, env)
		_, cancel, done := startServe(t, h)
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("exit = %d, want 0 with the embedded client id; stderr = %q", code, h.err.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serve did not stop")
		}
	}
}

func TestResolveDBPath(t *testing.T) {
	tests := []struct {
		name string
		flag string
		goos string
		env  map[string]string
		want string
	}{
		{"flag wins", "/flag.db", "linux", map[string]string{"EVE_MARKETS_DB": "/env.db", "XDG_DATA_HOME": "/xdg", "HOME": "/home/u"}, "/flag.db"},
		{"env var", "", "linux", map[string]string{"EVE_MARKETS_DB": "/env.db", "XDG_DATA_HOME": "/xdg", "HOME": "/home/u"}, "/env.db"},
		{"xdg data home", "", "linux", map[string]string{"XDG_DATA_HOME": "/xdg", "HOME": "/home/u"}, filepath.Join("/xdg", "eve-markets", "markets.db")},
		{"home fallback", "", "linux", map[string]string{"HOME": "/home/u"}, filepath.Join("/home/u", ".local", "share", "eve-markets", "markets.db")},
		{"linux ignores LOCALAPPDATA", "", "linux", map[string]string{"LOCALAPPDATA": "/local", "HOME": "/home/u"}, filepath.Join("/home/u", ".local", "share", "eve-markets", "markets.db")},
		{"darwin ignores LOCALAPPDATA", "", "darwin", map[string]string{"LOCALAPPDATA": "/local", "HOME": "/home/u"}, filepath.Join("/home/u", ".local", "share", "eve-markets", "markets.db")},
		{"windows flag wins", "/flag.db", "windows", map[string]string{"LOCALAPPDATA": "/local"}, "/flag.db"},
		{"windows env var", "", "windows", map[string]string{"EVE_MARKETS_DB": "/env.db", "LOCALAPPDATA": "/local"}, "/env.db"},
		{"windows xdg beats LOCALAPPDATA", "", "windows", map[string]string{"XDG_DATA_HOME": "/xdg", "LOCALAPPDATA": "/local"}, filepath.Join("/xdg", "eve-markets", "markets.db")},
		{"windows LOCALAPPDATA", "", "windows", map[string]string{"LOCALAPPDATA": "/local", "USERPROFILE": "/profile", "HOME": "/home/u"}, filepath.Join("/local", "eve-markets", "markets.db")},
		{"windows USERPROFILE fallback", "", "windows", map[string]string{"USERPROFILE": "/profile", "HOME": "/home/u"}, filepath.Join("/profile", "AppData", "Local", "eve-markets", "markets.db")},
		{"windows HOME last resort", "", "windows", map[string]string{"HOME": "/home/u"}, filepath.Join("/home/u", ".local", "share", "eve-markets", "markets.db")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDBPath(tt.flag, tt.goos, func(k string) string { return tt.env[k] })
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	for _, goos := range []string{"linux", "windows"} {
		if _, err := resolveDBPath("", goos, func(string) string { return "" }); err == nil {
			t.Errorf("%s with nothing set: want error", goos)
		}
	}
	if _, err := resolveDBPath("", "linux", func(k string) string { return map[string]string{"LOCALAPPDATA": "/local", "USERPROFILE": "/p"}[k] }); err == nil {
		t.Error("linux with only Windows variables: want error")
	}
}

func TestOpenSecureStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "data", "markets.db")
	s, err := openSecureStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Force WAL side files to exist.
	if err := s.UpsertUser(context.Background(), 1, "A", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v; want 0700", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if st, err := os.Stat(side); err == nil && st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", side, st.Mode().Perm())
		}
	}
}

func TestESIUserAgent(t *testing.T) {
	old := version
	version = "1.2.3"
	t.Cleanup(func() { version = old })
	if got := esiUserAgent(); got != "eve-markets/1.2.3 (local)" {
		t.Errorf("esiUserAgent() = %q", got)
	}
}

// startServe runs `serve` on a free port and returns the base URL, the
// cancel func and a channel that yields the exit code.
func startServe(t *testing.T, h *harness, extra ...string) (string, context.CancelFunc, <-chan int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	args := append([]string{"serve", "--addr", "127.0.0.1:0"}, extra...)
	go func() { done <- run(ctx, args, h.deps) }()
	select {
	case ln := <-h.listeners:
		return "http://" + ln.Addr().String(), cancel, done
	case code := <-done:
		t.Fatalf("serve exited early with %d: %s", code, h.err.String())
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start listening")
	}
	return "", nil, nil
}

func getJSON(t *testing.T, c *http.Client, url string) map[string]any {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestServeSignsInAndShutsDownCleanly(t *testing.T) {
	h := newHarness(t, serveEnv())
	base, cancel, done := startServe(t, h)
	c := signIn(t, base)

	me := getJSON(t, c, base+"/api/me")
	if me["character_id"] != float64(1) || me["name"] != "Alice" {
		t.Errorf("/api/me = %v", me)
	}
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/ status = %d", resp.StatusCode)
	}
	if !strings.Contains(h.out.String(), base) {
		t.Errorf("stdout should print the URL %s: %q", base, h.out.String())
	}
	if h.opened == nil || h.tokensFor != h.opened {
		t.Errorf("token source built from %p, opened store is %p", h.tokensFor, h.opened)
	}
	// A sign-in drops the cached access token of the character.
	if got := h.tokens.forgotten(); len(got) != 1 || got[0] != 1 {
		t.Errorf("forgotten tokens = %v, want [1]", got)
	}

	// Drop pooled keep-alive connections, including any spare the transport
	// dialed, so the graceful shutdown has nothing to wait for.
	c.CloseIdleConnections()
	http.DefaultClient.CloseIdleConnections()
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit = %d; stderr = %q", code, h.err.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
	if _, err := http.Get(base + "/"); err == nil {
		t.Error("server still accepting connections after shutdown")
	}
}

func TestServeHasNoShutdownEndpoint(t *testing.T) {
	h := newHarness(t, serveEnv())
	base, cancel, done := startServe(t, h)
	c := signIn(t, base)
	req, err := http.NewRequest(http.MethodPost, base+"/api/shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/shutdown status = %d, want 405", resp.StatusCode)
	}
	select {
	case code := <-done:
		t.Fatalf("server stopped with %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestSSOPortWarning(t *testing.T) {
	if got := ssoPortWarning("127.0.0.1:8089"); got != "" {
		t.Errorf("fixed port warned: %q", got)
	}
	for _, addr := range []string{"127.0.0.1:9000", "[::1]:0", "localhost:8088"} {
		got := ssoPortWarning(addr)
		if !strings.Contains(got, "SSO login will not work") || !strings.Contains(got, "8089") ||
			strings.Contains(got, "\n") || strings.TrimSpace(got) != got {
			t.Errorf("ssoPortWarning(%q) = %q, want a one-line warning naming port 8089", addr, got)
		}
	}
}

func TestServeWarnsWhenThePortIsNotTheRegisteredOne(t *testing.T) {
	h := newHarness(t, serveEnv())
	_, cancel, done := startServe(t, h) // 127.0.0.1:0: a random port
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit = %d; stderr = %q", code, h.err.String())
	}
	warnings := 0
	for _, line := range strings.Split(h.err.String(), "\n") {
		if strings.Contains(line, "SSO login will not work") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("stderr has %d port warnings, want 1: %q", warnings, h.err.String())
	}
	if strings.Contains(h.out.String(), "SSO login will not work") {
		t.Error("the warning belongs on stderr")
	}
}

func TestServeRejectsBadCollectInterval(t *testing.T) {
	for _, v := range []string{"30s", "-1m", "abc"} {
		h := newHarness(t, serveEnv())
		if got := run(context.Background(), []string{"serve", "--collect-interval", v}, h.deps); got != 2 {
			t.Errorf("%s: exit = %d, want 2", v, got)
		}
		select {
		case <-h.listeners:
			t.Errorf("%s: must not listen", v)
		default:
		}
	}
}

func TestServeAcceptsCollectIntervalZeroAndMinimum(t *testing.T) {
	for _, v := range []string{"0", "1m"} {
		h := newHarness(t, serveEnv())
		_, cancel, done := startServe(t, h, "--no-open", "--collect-interval", v)
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("%s: exit = %d; stderr = %q", v, code, h.err.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: serve did not stop", v)
		}
	}
}
