// Command eve-markets serves a local web page that explores what EVE Online
// ESI offers for public and private (structure) markets. Scaffolded from
// eve-wallets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/auth"
	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
	"github.com/escorbuto-petoruti/eve-markets/internal/sso"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
	"github.com/escorbuto-petoruti/eve-markets/internal/web"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	defaultAddr   = "127.0.0.1:8089"
	shutdownGrace = 10 * time.Second
)

const usage = `Usage:
  eve-markets help
  eve-markets version
  eve-markets update [--check] [--force]
  eve-markets serve [--addr 127.0.0.1:8089] [--db PATH] [--open | --no-open]

Commands:
  help     print this help
  version  print the version
  update   replace the binary with the latest GitHub release (linux and macOS)
  serve    serve the markets page on a loopback address;
           on Windows in a terminal it opens the page in your browser (--no-open stops it, --open forces it elsewhere)

Environment:
  EVE_MARKETS_CLIENT_ID  override the embedded EVE client id (optional; the application
                         must register http://localhost:8089/auth/callback)
  EVE_MARKETS_DB         database path (the --db flag wins)
  EVE_MARKETS_UPDATE_API GitHub API base URL for update (tests)
`

// deps are the collaborators of run, replaced by fakes in tests.
type deps struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	openStore      func(path string) (*store.Store, error)
	newTokens      func(st *store.Store) auth.TokenSource
	listen         func(network, addr string) (net.Listener, error)
	newSSO         func() web.SSO
	// goos, isTerminal and openBrowser decide and perform the browser launch of
	// serve; tests replace them so no real browser starts. Zero values mean
	// runtime.GOOS, no terminal and no opener.
	goos        string
	isTerminal  func() bool
	openBrowser func(url string) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], deps{
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		getenv:    os.Getenv,
		openStore: openSecureStore,
		newTokens: newStoreTokens,
		listen:    net.Listen,
		newSSO:    func() web.SSO { return sso.NewClient(ssoConfig(os.Getenv)) },

		goos:       runtime.GOOS,
		isTerminal: stdoutIsTerminal,
		openBrowser: func(url string) error {
			return startBrowser(runtime.GOOS, url)
		},
	})
	stop()
	os.Exit(code)
}

// run executes one command line and returns the process exit code.
func run(ctx context.Context, args []string, d deps) int {
	if len(args) == 0 {
		// No default action: print the help. Double-click start (Windows) is
		// deliberately unsupported.
		fmt.Fprint(d.stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(d.stdout, usage)
		return 0
	case "help":
		fmt.Fprint(d.stderr, usage)
		return 2
	case "version", "--version":
		fmt.Fprintf(d.stdout, "eve-markets %s\n", version)
		return 0
	case "update":
		return runUpdate(ctx, args[1:], d)
	case "serve":
		return runServe(ctx, args[1:], d)
	}
	fmt.Fprintf(d.stderr, "eve-markets: unknown command %q\n\n%s", args[0], usage)
	return 2
}

// parseFlags parses args into fset. It returns (code, true) when the command
// must stop: 0 for -h, 2 for a flag error.
func parseFlags(fset *flag.FlagSet, args []string) (int, bool) {
	switch err := fset.Parse(args); {
	case err == nil:
		return 0, false
	case errors.Is(err, flag.ErrHelp):
		return 0, true
	default:
		return 2, true
	}
}

func newFlagSet(name string, d deps) *flag.FlagSet {
	fset := flag.NewFlagSet("eve-markets "+name, flag.ContinueOnError)
	fset.SetOutput(d.stderr)
	return fset
}

// newStoreTokens builds the production token source: the refresh tokens kept
// in st, exchanged at EVE SSO with the embedded client.
func newStoreTokens(st *store.Store) auth.TokenSource {
	return auth.NewStoreTokens(st, sso.NewClient(ssoConfig(os.Getenv)), nil)
}

// clientIDEnv names the environment variable holding the EVE application id.
const clientIDEnv = "EVE_MARKETS_CLIENT_ID"

// ssoConfig is the SSO configuration. The client id is the embedded public
// client unless EVE_MARKETS_CLIENT_ID holds a non-blank override; the callback
// is fixed at http://localhost:8089/auth/callback, so an overriding application
// must register it.
func ssoConfig(getenv func(string) string) sso.Config {
	cfg := sso.DefaultConfig()
	if id := strings.TrimSpace(getenv(clientIDEnv)); id != "" {
		cfg.ClientID = id
	}
	return cfg
}

// esiUserAgent is the User-Agent the ESI client sends.
func esiUserAgent() string { return "eve-markets/" + version + " (local)" }

func runServe(ctx context.Context, args []string, d deps) int {
	fset := newFlagSet("serve", d)
	addr := fset.String("addr", defaultAddr, "listen address (loopback only)")
	dbFlag := fset.String("db", "", "database path")
	openFlag := fset.Bool("open", false, "open the page in the browser (default on Windows in a terminal)")
	noOpen := fset.Bool("no-open", false, "do not open the page in the browser")
	if code, stop := parseFlags(fset, args); stop {
		return code
	}
	if *openFlag && *noOpen {
		fmt.Fprintln(d.stderr, "eve-markets: --open and --no-open cannot be used together")
		return 2
	}
	if err := checkLoopbackAddr(*addr); err != nil {
		fmt.Fprintf(d.stderr, "eve-markets: %v\n", err)
		return 2
	}
	path, err := resolveDBPath(*dbFlag, runtime.GOOS, d.getenv)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-markets: %v\n", err)
		return 1
	}
	st, err := d.openStore(path)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-markets: %v\n", err)
		return 1
	}
	defer st.Close()

	ln, err := d.listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(d.stderr, "eve-markets: listen: %v\n", err)
		return 1
	}

	// A sign-in stores new credentials and scopes; the access token cached for
	// that character carries the old ones.
	var onTokenSaved func(int64)
	if f, ok := d.newTokens(st).(interface{ Forget(int64) }); ok {
		onTokenSaved = f.Forget
	}

	srv := &http.Server{
		Handler: web.New(web.Deps{
			Store:        st,
			SSO:          d.newSSO(),
			Markets:      esi.New(esi.Options{UserAgent: esiUserAgent()}),
			OnTokenSaved: onTokenSaved,
			AllowedPort:  listenPort(ln.Addr().String()),
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	fmt.Fprintf(d.stdout, "Serving on http://%s (database %s)\n", ln.Addr(), path)
	fmt.Fprintln(d.stdout, "Press Ctrl+C to stop.")
	if w := ssoPortWarning(ln.Addr().String()); w != "" {
		fmt.Fprintln(d.stderr, w)
	}
	goos := d.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	tty := d.isTerminal != nil && d.isTerminal()
	if shouldOpenBrowser(goos, tty, *openFlag, *noOpen, *addr) {
		// The listener is up, so the page is reachable on the real bound port.
		pageURL := browserURL(ln.Addr().String())
		var oerr error
		if d.openBrowser == nil {
			oerr = errors.New("no browser opener available")
		} else {
			oerr = d.openBrowser(pageURL)
		}
		if oerr != nil {
			fmt.Fprintf(d.stderr, "eve-markets: could not open the browser (%v); open %s yourself\n", oerr, pageURL)
		}
	}

	code := 0
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		fmt.Fprintf(d.stderr, "eve-markets: server: %v\n", err)
		code = 1
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		fmt.Fprintf(d.stderr, "eve-markets: shutdown: %v\n", err)
		code = 1
	}
	return code
}

// checkLoopbackAddr refuses any listen address that is not loopback. The API
// has no authentication, so there is deliberately no way around this check.
func checkLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %v", addr, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid --addr %q: bad port", addr)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing --addr %q: the API has no authentication, so only loopback addresses (127.0.0.1, ::1, localhost) are allowed", addr)
}

// resolveDBPath picks the database path: flag, EVE_MARKETS_DB,
// $XDG_DATA_HOME/eve-markets/markets.db, then on Windows
// %LOCALAPPDATA%\eve-markets\markets.db (or %USERPROFILE%\AppData\Local when
// LOCALAPPDATA is empty), and finally ~/.local/share/eve-markets/markets.db
// from $HOME. goos is a parameter so the choice is testable; callers pass
// runtime.GOOS.
func resolveDBPath(flagValue, goos string, getenv func(string) string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if p := getenv("EVE_MARKETS_DB"); p != "" {
		return p, nil
	}
	if x := getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "eve-markets", "markets.db"), nil
	}
	if goos == "windows" {
		if l := getenv("LOCALAPPDATA"); l != "" {
			return filepath.Join(l, "eve-markets", "markets.db"), nil
		}
		if u := getenv("USERPROFILE"); u != "" {
			return filepath.Join(u, "AppData", "Local", "eve-markets", "markets.db"), nil
		}
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "share", "eve-markets", "markets.db"), nil
	}
	return "", errors.New("cannot determine the database path: set --db, EVE_MARKETS_DB, HOME or (on Windows) LOCALAPPDATA")
}

// openSecureStore opens the database with a private directory (0700) and
// private files (0600), including the WAL side files.
func openSecureStore(path string) (*store.Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	// Create the file private first so SQLite's side files inherit the mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	_ = f.Close()
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = st.Close()
			return nil, fmt.Errorf("restrict database permissions: %w", err)
		}
	}
	return st, nil
}

// listenPort returns the port of a host:port address, or "" when it has none.
func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// ssoPortWarning returns a one-line warning when the server listens on a port
// other than the one in the fixed SSO redirect URL, or "" when they match.
func ssoPortWarning(listenAddr string) string {
	want := ""
	if u, err := url.Parse(sso.DefaultRedirectURL); err == nil {
		want = u.Port()
	}
	port := listenPort(listenAddr)
	if port == want {
		return ""
	}
	return fmt.Sprintf("eve-markets: warning: listening on port %s but the SSO redirect URL is registered for port %s, so SSO login will not work here (use --addr 127.0.0.1:%s)", port, want, want)
}
