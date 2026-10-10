package main

import (
	"testing"

	"github.com/escorbuto-petoruti/eve-markets/internal/sso"
)

func TestSSOConfigDefaultsWithoutOverride(t *testing.T) {
	cfg := ssoConfig(func(string) string { return "" })
	if cfg.ClientID == "" || cfg.ClientID != sso.DefaultClientID {
		t.Errorf("ClientID = %q, want the embedded default %q", cfg.ClientID, sso.DefaultClientID)
	}
	if cfg.RedirectURL != sso.DefaultRedirectURL {
		t.Errorf("RedirectURL = %q, want %q", cfg.RedirectURL, sso.DefaultRedirectURL)
	}
}

func TestSSOConfigClientIDOverride(t *testing.T) {
	env := map[string]string{"EVE_MARKETS_CLIENT_ID": "  my-own-client  "}
	cfg := ssoConfig(func(k string) string { return env[k] })
	if cfg.ClientID != "my-own-client" {
		t.Errorf("ClientID = %q, want the trimmed override", cfg.ClientID)
	}
	if cfg.RedirectURL != sso.DefaultRedirectURL {
		t.Errorf("the callback must stay %q, got %q", sso.DefaultRedirectURL, cfg.RedirectURL)
	}
	if len(cfg.Scopes) == 0 {
		t.Error("scopes must be kept")
	}
}

func TestSSOConfigBlankOverrideFallsBackToDefault(t *testing.T) {
	env := map[string]string{"EVE_MARKETS_CLIENT_ID": "   "}
	cfg := ssoConfig(func(k string) string { return env[k] })
	if cfg.ClientID != sso.DefaultClientID {
		t.Errorf("ClientID = %q, want the default for a blank override", cfg.ClientID)
	}
}
