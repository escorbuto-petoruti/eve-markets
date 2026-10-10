package collector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/auth"
	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

var t0 = time.Unix(1_000_000, 0)

func stored(id int64, buy bool, price float64, remain int64) store.StoredOrder {
	return store.StoredOrder{OrderID: id, TypeID: 34, IsBuy: buy, Price: price, VolumeRemain: remain, VolumeTotal: 100,
		Issued: "2026-01-01T00:00:00Z", Duration: 90, FirstSeen: t0.Add(-time.Hour), LastSeen: t0.Add(-time.Hour)}
}

func live(id int64, buy bool, price float64, remain int64) esi.Order {
	return esi.Order{OrderID: id, TypeID: 34, IsBuyOrder: buy, Price: price, VolumeRemain: remain, VolumeTotal: 100,
		Issued: "2026-01-01T00:00:00Z", Duration: 90}
}

func kinds(ev []store.OrderEvent) []string {
	var out []string
	for _, e := range ev {
		out = append(out, fmt.Sprintf("%s:%d:%d", e.Kind, e.OrderID, e.DeltaVolume))
	}
	slices.Sort(out)
	return out
}

func TestDiffFirstCollectionHasNoBaselineNoise(t *testing.T) {
	up, del, ev := Diff(nil, []esi.Order{live(1, true, 5, 10), live(2, false, 9, 3)}, t0)
	if len(up) != 2 || len(del) != 0 || len(ev) != 0 {
		t.Fatalf("up=%d del=%d ev=%v, want 2,0,none", len(up), len(del), ev)
	}
	for _, o := range up {
		if !o.FirstSeen.Equal(t0) || !o.LastSeen.Equal(t0) {
			t.Errorf("times = %v %v", o.FirstSeen, o.LastSeen)
		}
	}
}

func TestDiffRules(t *testing.T) {
	prev := []store.StoredOrder{
		stored(1, true, 5, 10),  // unchanged
		stored(2, true, 5, 10),  // fill
		stored(3, true, 5, 10),  // price change only
		stored(4, true, 5, 10),  // volume up, same price: update, no event
		stored(5, false, 7, 10), // gone
		stored(6, true, 5, 10),  // fill and price change
	}
	now := []esi.Order{
		live(1, true, 5, 10),
		live(2, true, 5, 6),
		live(3, true, 6, 10),
		live(4, true, 5, 12),
		live(6, true, 4, 8),
		live(7, true, 3, 20), // new
	}
	up, del, ev := Diff(prev, now, t0)
	wantEv := []string{"fill:2:4", "fill:6:2", "gone:5:10", "new:7:20", "price:3:0", "price:6:0"}
	if got := kinds(ev); !slices.Equal(got, wantEv) {
		t.Errorf("events = %v, want %v", got, wantEv)
	}
	if !slices.Equal(del, []int64{5}) {
		t.Errorf("deletes = %v", del)
	}
	gotUp := map[int64]store.StoredOrder{}
	for _, o := range up {
		gotUp[o.OrderID] = o
	}
	if len(gotUp) != 5 { // 2,3,4,6,7 (1 is unchanged)
		t.Fatalf("upserts = %v", gotUp)
	}
	if _, ok := gotUp[1]; ok {
		t.Error("unchanged order upserted")
	}
	if o := gotUp[2]; o.VolumeRemain != 6 || !o.FirstSeen.Equal(t0.Add(-time.Hour)) || !o.LastSeen.Equal(t0) {
		t.Errorf("order 2 = %+v", o)
	}
	if o := gotUp[4]; o.VolumeRemain != 12 {
		t.Errorf("order 4 = %+v", o)
	}
	if o := gotUp[7]; !o.FirstSeen.Equal(t0) {
		t.Errorf("order 7 = %+v", o)
	}
	for _, e := range ev {
		if !e.At.Equal(t0) {
			t.Errorf("event time = %v", e.At)
		}
		if e.Kind == "price" && e.Price == 0 {
			t.Errorf("price event without price: %+v", e)
		}
	}
}

// fakes

type fakeStore struct {
	watched []store.WatchedStructure
	current map[int64][]store.StoredOrder
	applied []applyCall
	runs    []runCall
	applyEr error
}

type applyCall struct {
	structure int64
	upserts   []store.StoredOrder
	deletes   []int64
	events    []store.OrderEvent
}

type runCall struct {
	char, structure int64
	status, errMsg  string
	at              time.Time
}

func (f *fakeStore) ListWatched(context.Context) ([]store.WatchedStructure, error) {
	return f.watched, nil
}
func (f *fakeStore) CurrentOrders(_ context.Context, id int64) ([]store.StoredOrder, error) {
	return f.current[id], nil
}
func (f *fakeStore) ApplyOrderDiff(_ context.Context, id int64, _ time.Time, up []store.StoredOrder, del []int64, ev []store.OrderEvent) error {
	f.applied = append(f.applied, applyCall{id, up, del, ev})
	return f.applyEr
}
func (f *fakeStore) RecordRun(_ context.Context, c, s int64, at time.Time, status, msg string) error {
	f.runs = append(f.runs, runCall{c, s, status, msg, at})
	return nil
}

type fakeESI struct {
	orders    map[int64][]esi.Order
	truncated bool
	err       error
	errByTok  map[string]error // optional per-token failure, checked before err
	calls     []int64
	tokens    []string
}

func (f *fakeESI) StructureOrders(_ context.Context, token string, id int64) ([]esi.Order, bool, error) {
	f.calls = append(f.calls, id)
	f.tokens = append(f.tokens, token)
	if err, ok := f.errByTok[token]; ok {
		return nil, false, err
	}
	return f.orders[id], f.truncated, f.err
}

type fakeTokens struct{ err error }

func (f fakeTokens) Characters(context.Context) ([]auth.Character, error) { return nil, nil }
func (f fakeTokens) Token(context.Context, int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "secret-access-token", nil
}

func newDeps(fs *fakeStore, fe *fakeESI, tk fakeTokens) Deps {
	return Deps{Store: fs, ESI: fe, Tokens: tk, Now: func() time.Time { return t0 }}
}

var w1 = store.WatchedStructure{CharacterID: 1, StructureID: 500}

func TestCollectOKAppliesDiff(t *testing.T) {
	fs := &fakeStore{current: map[int64][]store.StoredOrder{500: {stored(1, true, 5, 10), stored(2, true, 5, 10)}}}
	fe := &fakeESI{orders: map[int64][]esi.Order{500: {live(1, true, 5, 4)}}}
	if err := Collect(context.Background(), newDeps(fs, fe, fakeTokens{}), w1); err != nil {
		t.Fatal(err)
	}
	if len(fs.applied) != 1 {
		t.Fatalf("applied = %d", len(fs.applied))
	}
	if got := kinds(fs.applied[0].events); !slices.Equal(got, []string{"fill:1:6", "gone:2:10"}) {
		t.Errorf("events = %v", got)
	}
	if fe.tokens[0] != "secret-access-token" {
		t.Errorf("token = %q", fe.tokens[0])
	}
	if len(fs.runs) != 1 || fs.runs[0].status != StatusOK || fs.runs[0].errMsg != "" || !fs.runs[0].at.Equal(t0) {
		t.Errorf("runs = %+v", fs.runs)
	}
}

func TestCollectFirstRunStoresWithoutEvents(t *testing.T) {
	fs := &fakeStore{}
	fe := &fakeESI{orders: map[int64][]esi.Order{500: {live(1, true, 5, 4)}}}
	if err := Collect(context.Background(), newDeps(fs, fe, fakeTokens{}), w1); err != nil {
		t.Fatal(err)
	}
	if len(fs.applied) != 1 || len(fs.applied[0].upserts) != 1 || len(fs.applied[0].events) != 0 {
		t.Errorf("applied = %+v", fs.applied)
	}
}

func TestCollectTruncatedNeverDeletes(t *testing.T) {
	fs := &fakeStore{current: map[int64][]store.StoredOrder{500: {stored(1, true, 5, 10), stored(2, true, 5, 10)}}}
	fe := &fakeESI{orders: map[int64][]esi.Order{500: {live(1, true, 5, 4)}}, truncated: true}
	if err := Collect(context.Background(), newDeps(fs, fe, fakeTokens{}), w1); err != nil {
		t.Fatal(err)
	}
	a := fs.applied[0]
	if len(a.deletes) != 0 {
		t.Errorf("deletes = %v", a.deletes)
	}
	for _, e := range a.events {
		if e.Kind == store.EventGone {
			t.Errorf("gone event on truncated fetch: %+v", e)
		}
	}
	if got := kinds(a.events); !slices.Equal(got, []string{"fill:1:6"}) {
		t.Errorf("events = %v", got)
	}
	if fs.runs[0].status != StatusTruncated {
		t.Errorf("status = %q", fs.runs[0].status)
	}
}

func TestCollectFailureStatuses(t *testing.T) {
	tests := []struct {
		name    string
		esiErr  error
		tokErr  error
		status  string
		wantRet bool // Collect returns an error
	}{
		{"forbidden", &esi.APIError{Status: 403}, nil, StatusForbidden, false},
		{"reauth", nil, &auth.ReauthError{CharacterID: 1, Name: "x"}, StatusNeedsReauth, false},
		{"rate limited", &esi.RateLimitError{Status: 429, RetryAfter: time.Minute}, nil, StatusRateLimited, true},
		{"generic esi", errors.New("boom secret-access-token"), nil, StatusError, false},
		{"generic token", nil, errors.New("db down"), StatusError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeStore{}
			fe := &fakeESI{err: tt.esiErr}
			err := Collect(context.Background(), newDeps(fs, fe, fakeTokens{err: tt.tokErr}), w1)
			if (err != nil) != tt.wantRet {
				t.Errorf("returned %v, wantRet %v", err, tt.wantRet)
			}
			if tt.wantRet {
				var rl *esi.RateLimitError
				if !errors.As(err, &rl) || rl.RetryAfter != time.Minute {
					t.Errorf("err = %v, want the rate limit error", err)
				}
			}
			if len(fs.runs) != 1 || fs.runs[0].status != tt.status {
				t.Fatalf("runs = %+v, want status %s", fs.runs, tt.status)
			}
			if len(fs.applied) != 0 {
				t.Errorf("applied on failure: %+v", fs.applied)
			}
			if tt.status == StatusError && fs.runs[0].errMsg == "" {
				t.Error("error status without message")
			}
			if m := fs.runs[0].errMsg; len(m) > 0 && (strings.Contains(m, "secret-access-token") || strings.Contains(m, "boom")) {
				t.Errorf("error message leaks detail: %q", m)
			}
		})
	}
}

func TestCollectContextCancelRecordsNothing(t *testing.T) {
	fs := &fakeStore{}
	fe := &fakeESI{err: context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Collect(ctx, newDeps(fs, fe, fakeTokens{}), w1)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if len(fs.runs) != 0 {
		t.Errorf("runs = %+v", fs.runs)
	}
}

func TestRunIteratesSeriallyAndStopsOnRateLimit(t *testing.T) {
	fs := &fakeStore{watched: []store.WatchedStructure{
		{CharacterID: 1, StructureID: 500}, {CharacterID: 1, StructureID: 600}, {CharacterID: 1, StructureID: 700},
	}}
	fe := &fakeESI{orders: map[int64][]esi.Order{}}
	if err := Run(context.Background(), newDeps(fs, fe, fakeTokens{})); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fe.calls, []int64{500, 600, 700}) {
		t.Errorf("calls = %v", fe.calls)
	}

	fs2 := &fakeStore{watched: fs.watched}
	fe2 := &fakeESI{err: &esi.RateLimitError{Status: 429, RetryAfter: time.Minute}}
	err := Run(context.Background(), newDeps(fs2, fe2, fakeTokens{}))
	var rl *esi.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("Run err = %v, want rate limit", err)
	}
	if len(fe2.calls) != 1 {
		t.Errorf("calls after rate limit = %v, want 1", fe2.calls)
	}
}

// Shared structures: one fetch per structure, many watchers.

type tokenByChar struct{}

func (tokenByChar) Characters(context.Context) ([]auth.Character, error) { return nil, nil }
func (tokenByChar) Token(_ context.Context, id int64) (string, error) {
	return fmt.Sprintf("token-%d", id), nil
}

func sharedDeps(fs *fakeStore, fe *fakeESI) Deps {
	return Deps{Store: fs, ESI: fe, Tokens: tokenByChar{}, Now: func() time.Time { return t0 }}
}

func twoWatchers() []store.WatchedStructure {
	return []store.WatchedStructure{{CharacterID: 1, StructureID: 500}, {CharacterID: 2, StructureID: 500}}
}

func runStatuses(runs []runCall) []string {
	var out []string
	for _, r := range runs {
		out = append(out, fmt.Sprintf("%d/%d:%s", r.char, r.structure, r.status))
	}
	return out
}

func TestRunFetchesSharedStructureOnce(t *testing.T) {
	fs := &fakeStore{watched: twoWatchers()}
	fe := &fakeESI{orders: map[int64][]esi.Order{500: {live(1, true, 5, 4)}}}
	if err := Run(context.Background(), sharedDeps(fs, fe)); err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) != 1 || fe.tokens[0] != "token-1" {
		t.Errorf("fetches = %v tokens = %v, want one fetch with token-1", fe.calls, fe.tokens)
	}
	if len(fs.applied) != 1 {
		t.Errorf("applied = %d, want 1", len(fs.applied))
	}
	if got := runStatuses(fs.runs); !slices.Equal(got, []string{"1/500:ok", "2/500:ok"}) {
		t.Errorf("runs = %v", got)
	}
}

func TestRunSharedStructureFallsThroughOnReauthAndForbidden(t *testing.T) {
	tests := []struct {
		name string
		deps func(fs *fakeStore, fe *fakeESI) Deps
		fe   *fakeESI
		want []string
	}{
		{"reauth", func(fs *fakeStore, fe *fakeESI) Deps {
			d := sharedDeps(fs, fe)
			d.Tokens = fakeTokensByChar{reauth: 1}
			return d
		}, &fakeESI{orders: map[int64][]esi.Order{500: {live(1, true, 5, 4)}}}, []string{"1/500:needs_reauth", "2/500:ok"}},
		{"forbidden", sharedDeps, &fakeESI{
			orders:   map[int64][]esi.Order{500: {live(1, true, 5, 4)}},
			errByTok: map[string]error{"token-1": &esi.APIError{Status: 403}},
		}, []string{"1/500:forbidden", "2/500:ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeStore{watched: twoWatchers()}
			if err := Run(context.Background(), tt.deps(fs, tt.fe)); err != nil {
				t.Fatal(err)
			}
			if got := runStatuses(fs.runs); !slices.Equal(got, tt.want) {
				t.Errorf("runs = %v, want %v", got, tt.want)
			}
			if n := len(tt.fe.tokens); n == 0 || tt.fe.tokens[n-1] != "token-2" {
				t.Errorf("tokens = %v, want the second watcher's token last", tt.fe.tokens)
			}
			if len(fs.applied) != 1 {
				t.Errorf("applied = %d, want 1", len(fs.applied))
			}
		})
	}
}

// fakeTokensByChar fails with a reauth error for one character only.
type fakeTokensByChar struct{ reauth int64 }

func (fakeTokensByChar) Characters(context.Context) ([]auth.Character, error) { return nil, nil }
func (f fakeTokensByChar) Token(_ context.Context, id int64) (string, error) {
	if id == f.reauth {
		return "", &auth.ReauthError{CharacterID: id, Name: "x"}
	}
	return fmt.Sprintf("token-%d", id), nil
}

func TestRunSharedStructureAllWatchersFail(t *testing.T) {
	fs := &fakeStore{watched: twoWatchers()}
	fe := &fakeESI{errByTok: map[string]error{"token-1": &esi.APIError{Status: 403}, "token-2": &esi.APIError{Status: 403}}}
	if err := Run(context.Background(), sharedDeps(fs, fe)); err != nil {
		t.Fatal(err)
	}
	if got := runStatuses(fs.runs); !slices.Equal(got, []string{"1/500:forbidden", "2/500:forbidden"}) {
		t.Errorf("runs = %v", got)
	}
	if len(fs.applied) != 0 {
		t.Errorf("applied on failure: %+v", fs.applied)
	}
}

func TestRunSharedStructureRateLimitDoesNotFallThrough(t *testing.T) {
	fs := &fakeStore{watched: twoWatchers()}
	fe := &fakeESI{err: &esi.RateLimitError{Status: 429, RetryAfter: time.Minute}}
	err := Run(context.Background(), sharedDeps(fs, fe))
	var rl *esi.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want rate limit", err)
	}
	if len(fe.calls) != 1 {
		t.Errorf("fetches = %d, want 1", len(fe.calls))
	}
	if got := runStatuses(fs.runs); !slices.Equal(got, []string{"1/500:rate_limited", "2/500:rate_limited"}) {
		t.Errorf("runs = %v", got)
	}
}

func TestRunSharedStructureGenericErrorDoesNotFallThrough(t *testing.T) {
	fs := &fakeStore{watched: twoWatchers()}
	fe := &fakeESI{err: errors.New("boom")}
	if err := Run(context.Background(), sharedDeps(fs, fe)); err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) != 1 {
		t.Errorf("fetches = %d, want 1", len(fe.calls))
	}
	if got := runStatuses(fs.runs); !slices.Equal(got, []string{"1/500:error", "2/500:error"}) {
		t.Errorf("runs = %v", got)
	}
}

func TestRunGroupsOnlyAdjacentSameStructure(t *testing.T) {
	fs := &fakeStore{watched: []store.WatchedStructure{
		{CharacterID: 1, StructureID: 500}, {CharacterID: 2, StructureID: 500}, {CharacterID: 1, StructureID: 600},
	}}
	fe := &fakeESI{orders: map[int64][]esi.Order{}}
	if err := Run(context.Background(), sharedDeps(fs, fe)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fe.calls, []int64{500, 600}) {
		t.Errorf("fetches = %v", fe.calls)
	}
	if len(fs.runs) != 3 {
		t.Errorf("runs = %v", runStatuses(fs.runs))
	}
}
