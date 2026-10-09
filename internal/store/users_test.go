package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0).UTC()

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestForeignKeysEnforcedOnEveryConnection(t *testing.T) {
	s := openTemp(t)
	// Hold several connections open at once so the pool must create more than one.
	var conns []*sql.Conn
	for i := 0; i < 3; i++ {
		c, err := s.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		var on int
		if err := c.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
			t.Fatalf("foreign_keys = %d, err = %v, want 1", on, err)
		}
		_ = c.Close()
	}
	// A token for a user that does not exist must be refused.
	err := s.SaveToken(context.Background(), Token{CharacterID: 1, UserID: 1, CharacterName: "x", RefreshToken: "r", Scopes: []string{"a"}})
	if err == nil {
		t.Fatal("SaveToken accepted a token for a missing user")
	}
}

func TestUpsertUserKeepsCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 7, "Old", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertUser(ctx, 7, "New", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var name string
	var created int64
	if err := s.db.QueryRow(`SELECT name, created_at FROM users WHERE character_id = 7`).Scan(&name, &created); err != nil {
		t.Fatal(err)
	}
	if name != "New" || created != t0.Unix() {
		t.Fatalf("name = %q created_at = %d, want New and %d", name, created, t0.Unix())
	}
	if n := count(t, s, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

func TestTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 1, "Alice", t0); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetToken(ctx, 1); err != nil || ok {
		t.Fatalf("GetToken on empty = ok %v, err %v", ok, err)
	}
	tok := Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: "first",
		Scopes: []string{"esi-markets.structure_markets.v1", "esi-search.search_structures.v1"}, UpdatedAt: t0}
	if err := s.SaveToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetToken(ctx, 1)
	if err != nil || !ok || !reflect.DeepEqual(got, tok) {
		t.Fatalf("GetToken = %+v, ok %v, err %v, want %+v", got, ok, err, tok)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT scopes FROM tokens WHERE character_id = 1`).Scan(&raw); err != nil ||
		raw != "esi-markets.structure_markets.v1 esi-search.search_structures.v1" {
		t.Fatalf("scopes stored as %q, err %v", raw, err)
	}

	tok.RefreshToken, tok.UpdatedAt = "second", t0.Add(time.Minute)
	if err := s.SaveToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetToken(ctx, 1)
	if got.RefreshToken != "second" || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("upsert did not replace the token: %+v", got)
	}
	if n := count(t, s, `SELECT count(*) FROM tokens`); n != 1 {
		t.Fatalf("tokens = %d, want 1", n)
	}

	// Empty scopes round-trip as an empty slice, not [""].
	if err := s.UpsertUser(ctx, 2, "Bob", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, Token{CharacterID: 2, UserID: 2, CharacterName: "Bob", RefreshToken: "b", UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Tokens(ctx)
	if err != nil || len(all) != 2 || all[0].CharacterID != 1 || all[1].CharacterID != 2 || len(all[1].Scopes) != 0 {
		t.Fatalf("Tokens = %+v, err %v", all, err)
	}

	if err := s.DeleteToken(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetToken(ctx, 1); ok {
		t.Fatal("token still present after DeleteToken")
	}
	if err := s.DeleteToken(ctx, 1); err != nil {
		t.Fatalf("DeleteToken of a missing token should be a no-op: %v", err)
	}
}

func TestTokenErrorsNeverContainTheRefreshToken(t *testing.T) {
	s := openTemp(t)
	err := s.SaveToken(context.Background(), Token{CharacterID: 1, UserID: 99, CharacterName: "x", RefreshToken: "SECRET-VALUE"})
	if err == nil || strings.Contains(err.Error(), "SECRET-VALUE") {
		t.Fatalf("err = %v, want an error without the secret", err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 1, "Alice", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "live", 1, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "old", 1, t0.Add(-2*time.Hour), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	u, ok, err := s.SessionUser(ctx, "live", t0)
	if err != nil || !ok || u.CharacterID != 1 || u.Name != "Alice" {
		t.Fatalf("SessionUser(live) = %+v, ok %v, err %v", u, ok, err)
	}
	if _, ok, err := s.SessionUser(ctx, "old", t0); err != nil || ok {
		t.Fatalf("expired session returned: ok %v, err %v", ok, err)
	}
	if _, ok, _ := s.SessionUser(ctx, "live", t0.Add(time.Hour)); ok {
		t.Fatal("session valid at exactly expires_at")
	}
	if _, ok, _ := s.SessionUser(ctx, "missing", t0); ok {
		t.Fatal("unknown session returned")
	}

	n, err := s.PurgeExpiredSessions(ctx, t0)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpiredSessions = %d, err %v, want 1", n, err)
	}
	if c := count(t, s, `SELECT count(*) FROM sessions`); c != 1 {
		t.Fatalf("sessions = %d, want 1", c)
	}
	if err := s.DeleteSession(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SessionUser(ctx, "live", t0); ok {
		t.Fatal("session survives DeleteSession")
	}
}

// The session user must carry the identifier characters are keyed by
// (sessions.user_id, which tokens.user_id references too), so callers
// never have to infer a user id from a character id: today the two are equal
// only because users.character_id is the primary key the FKs point at.
func TestSessionUserCarriesUserID(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.UpsertUser(ctx, 42, "Dana", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "dana", 42, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	u, ok, err := s.SessionUser(ctx, "dana", t0)
	if err != nil || !ok || u.CharacterID != 42 || u.Name != "Dana" {
		t.Fatalf("SessionUser(dana) = %+v, ok %v, err %v", u, ok, err)
	}
	if u.UserID != 42 {
		t.Fatalf("SessionUser(dana).UserID = %d, want 42 (sessions.user_id)", u.UserID)
	}
	chars, err := s.CharactersForUser(ctx, u.UserID)
	if err != nil || len(chars) != 0 {
		t.Fatalf("CharactersForUser(SessionUser.UserID) = %v, err %v, want none yet", chars, err)
	}
}

func TestCascadeDeletes(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_ = s.UpsertUser(ctx, 1, "Alice", t0)
	_ = s.SaveToken(ctx, Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: "r", UpdatedAt: t0})
	_ = s.CreateSession(ctx, "s", 1, t0, t0.Add(time.Hour))

	// User deleted -> tokens and sessions gone.
	if _, err := s.db.Exec(`DELETE FROM users WHERE character_id = 1`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tokens", "sessions"} {
		if n := count(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s rows after user delete = %d", table, n)
		}
	}
}

// seedMoveFixture registers Alice (user 1, characters 1 and 3) and Bob (user 2,
// character 2), each character with a stored token.
func seedMoveFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, u := range []struct {
		id   int64
		name string
	}{{1, "Alice"}, {2, "Bob"}} {
		if err := s.UpsertUser(ctx, u.id, u.name, t0); err != nil {
			t.Fatal(err)
		}
	}
	chars := []struct {
		id, user int64
		name     string
	}{{1, 1, "Alice"}, {3, 1, "Alice Alt"}, {2, 2, "Bob"}}
	for _, c := range chars {
		tok := Token{CharacterID: c.id, UserID: c.user, CharacterName: c.name, RefreshToken: "secret-" + c.name,
			Scopes: []string{"esi-markets.structure_markets.v1"}, UpdatedAt: t0}
		if err := s.SaveToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
}

// charIDs returns the ids of the user's characters, sorted.
func charIDs(t *testing.T, s *Store, userID int64) []int64 {
	t.Helper()
	cs, err := s.CharactersForUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range cs {
		ids = append(ids, c.CharacterID)
	}
	return ids
}

func TestTokenOwner(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s)
	if u, ok, err := s.TokenOwner(ctx, 3); err != nil || !ok || u != 1 {
		t.Fatalf("TokenOwner(3) = %d, ok %v, err %v, want 1", u, ok, err)
	}
	if u, ok, err := s.TokenOwner(ctx, 99); err != nil || ok || u != 0 {
		t.Fatalf("TokenOwner(99) = %d, ok %v, err %v, want not found", u, ok, err)
	}
}

func TestCharactersForUser(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s)
	got, err := s.CharactersForUser(ctx, 1)
	want := []UserCharacter{{CharacterID: 1, Name: "Alice"}, {CharacterID: 3, Name: "Alice Alt"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("CharactersForUser(1) = %+v, err %v, want %+v", got, err, want)
	}
	if got, err := s.CharactersForUser(ctx, 99); err != nil || len(got) != 0 {
		t.Fatalf("CharactersForUser(99) = %+v, err %v, want empty", got, err)
	}
	// UserCharacter carries no token material by construction.
	typ := reflect.TypeOf(UserCharacter{})
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i).Name; f != "CharacterID" && f != "Name" {
			t.Errorf("UserCharacter has unexpected field %s", f)
		}
	}
}

func TestMoveTokenKeepsPreviousUserWithOtherCharacters(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s)
	if err := s.MoveToken(ctx, 3, 2); err != nil {
		t.Fatal(err)
	}
	tok, ok, err := s.GetToken(ctx, 3)
	if err != nil || !ok || tok.UserID != 2 || tok.RefreshToken != "secret-Alice Alt" ||
		!reflect.DeepEqual(tok.Scopes, []string{"esi-markets.structure_markets.v1"}) {
		t.Fatalf("moved token = %+v, ok %v, err %v", tok, ok, err)
	}
	if n := count(t, s, `SELECT count(*) FROM users WHERE character_id = 1`); n != 1 {
		t.Fatalf("previous user rows = %d, want 1", n)
	}
	if got, want := charIDs(t, s, 1), []int64{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("previous user characters = %v, want %v", got, want)
	}
	if got, want := charIDs(t, s, 2), []int64{2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new user characters = %v, want %v", got, want)
	}
}

func TestMoveTokenDeletesEmptiedPreviousUser(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s)
	if err := s.CreateSession(ctx, "bob-session", 2, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveToken(ctx, 2, 1); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM users WHERE character_id = 2`); n != 0 {
		t.Fatalf("emptied user rows = %d, want 0", n)
	}
	if n := count(t, s, `SELECT count(*) FROM sessions WHERE user_id = 2`); n != 0 {
		t.Fatalf("sessions of the deleted user = %d, want 0", n)
	}
	if got, want := charIDs(t, s, 1), []int64{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new user characters = %v, want %v", got, want)
	}
}

func TestMoveTokenToSameOwnerIsNoop(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s)
	if err := s.MoveToken(ctx, 3, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := charIDs(t, s, 1), []int64{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("characters = %v, want %v", got, want)
	}
	if u, _, _ := s.TokenOwner(ctx, 3); u != 1 {
		t.Fatalf("owner = %d, want 1", u)
	}
}

func TestMoveTokenFailuresChangeNothing(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s)
	snapshot := func() [3]int {
		return [3]int{
			count(t, s, `SELECT count(*) FROM users`),
			count(t, s, `SELECT count(*) FROM tokens WHERE user_id = 1`),
			count(t, s, `SELECT count(*) FROM tokens`),
		}
	}
	before := snapshot()
	for _, tc := range []struct {
		name      string
		character int64
		toUser    int64
	}{
		{"unknown character", 99, 2},
		{"unknown target user", 3, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.MoveToken(ctx, tc.character, tc.toUser)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("MoveToken error = %v, want ErrNotFound", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error leaks token material: %v", err)
			}
			if after := snapshot(); after != before {
				t.Fatalf("state changed: %v -> %v", before, after)
			}
			if u, ok, _ := s.TokenOwner(ctx, 3); !ok || u != 1 {
				t.Fatalf("owner of 3 = %d, ok %v, want 1", u, ok)
			}
		})
	}
}

func TestSaveTokenIfOwner(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedMoveFixture(t, s) // character 3 belongs to user 1, character 2 to user 2

	save := func(char, user int64, name, secret string) bool {
		t.Helper()
		applied, err := s.SaveTokenIfOwner(ctx, Token{
			CharacterID: char, UserID: user, CharacterName: name, RefreshToken: secret,
			Scopes: []string{"new.scope"}, UpdatedAt: t0,
		})
		if err != nil {
			t.Fatal(err)
		}
		return applied
	}

	if !save(9, 1, "Fresh", "fresh-secret") {
		t.Error("a new character was not applied")
	}
	tok, ok, _ := s.GetToken(ctx, 9)
	if !ok || tok.UserID != 1 || tok.RefreshToken != "fresh-secret" || tok.CharacterName != "Fresh" {
		t.Errorf("new token = %+v ok=%v", tok, ok)
	}

	if !save(3, 1, "Renamed", "rotated") {
		t.Error("the owner's own character was not applied")
	}
	tok, _, _ = s.GetToken(ctx, 3)
	if tok.UserID != 1 || tok.RefreshToken != "rotated" || tok.CharacterName != "Renamed" ||
		!reflect.DeepEqual(tok.Scopes, []string{"new.scope"}) {
		t.Errorf("refreshed token = %+v", tok)
	}

	before, _, _ := s.GetToken(ctx, 2)
	if save(2, 1, "Thief", "stolen") {
		t.Error("another user's character was applied")
	}
	after, _, _ := s.GetToken(ctx, 2)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("foreign token changed: %+v -> %+v", before, after)
	}
}
