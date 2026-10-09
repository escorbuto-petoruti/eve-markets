package sso

import (
	"reflect"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ClientID != "" {
		t.Errorf("ClientID = %q, want empty (supplied by EVE_MARKETS_CLIENT_ID)", cfg.ClientID)
	}
	if cfg.RedirectURL != "http://localhost:8089/auth/callback" {
		t.Errorf("RedirectURL = %q", cfg.RedirectURL)
	}
	want := []string{
		"esi-markets.structure_markets.v1",
		"esi-search.search_structures.v1",
		"esi-universe.read_structures.v1",
	}
	if !reflect.DeepEqual(cfg.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", cfg.Scopes, want)
	}
}

func TestDefaultConfigLeavesOverridableFieldsZero(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.LoginBaseURL != "" || cfg.HTTPClient != nil || cfg.AllowedIssuers != nil {
		t.Errorf("expected zero overridable fields, got %+v", cfg)
	}
}

func TestDefaultConfigScopesAreNotShared(t *testing.T) {
	a := DefaultConfig()
	a.Scopes[0] = "mutated"
	if b := DefaultConfig(); b.Scopes[0] == "mutated" {
		t.Error("DefaultConfig must return a fresh Scopes slice")
	}
}
