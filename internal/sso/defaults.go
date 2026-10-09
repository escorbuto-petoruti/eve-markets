package sso

const (
	// DefaultClientID is the embedded EVE application id. It is a public PKCE
	// client id, not a secret; EVE_MARKETS_CLIENT_ID overrides it.
	DefaultClientID = "cda14518a2b4474499c44f0ee75cf942"
	// DefaultRedirectURL is the callback the EVE application must register.
	DefaultRedirectURL = "http://localhost:8089/auth/callback"
)

// Market scopes.
const (
	scopeStructureMarkets = "esi-markets.structure_markets.v1"
	scopeSearchStructures = "esi-search.search_structures.v1"
	scopeReadStructures   = "esi-universe.read_structures.v1"
)

// MarketScopes returns the scopes needed to discover structures and read their
// markets. It returns a fresh slice so callers cannot mutate shared state.
func MarketScopes() []string {
	return []string{scopeStructureMarkets, scopeSearchStructures, scopeReadStructures}
}

// DefaultConfig returns the embedded client configuration. ClientID is the
// embedded public client. Every other field is zero so tests and callers can
// override it.
func DefaultConfig() Config {
	return Config{
		ClientID:    DefaultClientID,
		RedirectURL: DefaultRedirectURL,
		Scopes:      MarketScopes(),
	}
}
