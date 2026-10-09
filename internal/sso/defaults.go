package sso

const (
	// DefaultClientID is empty on purpose: every user registers their own EVE
	// application (PKCE public client) and passes its id through the
	// EVE_MARKETS_CLIENT_ID environment variable.
	DefaultClientID = ""
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

// DefaultConfig returns the embedded client configuration. ClientID is empty
// until the caller supplies one. Every other field is zero so tests and callers
// can override it.
func DefaultConfig() Config {
	return Config{
		ClientID:    DefaultClientID,
		RedirectURL: DefaultRedirectURL,
		Scopes:      MarketScopes(),
	}
}
