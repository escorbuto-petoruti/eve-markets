// Package collector snapshots the orders of watched structures and turns the
// difference between two snapshots into events. ESI has no market history, so
// demand is inferred from these events.
package collector

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/auth"
	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
	"github.com/escorbuto-petoruti/eve-markets/internal/store"
)

// Collection outcomes stored in watched_structures.last_status.
const (
	StatusOK          = "ok"
	StatusTruncated   = "truncated"
	StatusForbidden   = "forbidden"
	StatusNeedsReauth = "needs_reauth"
	StatusRateLimited = "rate_limited"
	StatusError       = "error"
)

// Fixed messages: details go to the log, never to the database or the UI.
const (
	msgTruncated   = "the market has more pages than the page cap; orders were updated but none was marked gone"
	msgForbidden   = "no market access to this structure (docking/ACL)"
	msgNeedsReauth = "this character must sign in again"
	msgRateLimited = "ESI rate limited the collector; it will retry later"
	msgError       = "could not collect orders; see the server log"
)

// Store is the persistence the collector needs; *store.Store implements it.
type Store interface {
	ListWatched(ctx context.Context) ([]store.WatchedStructure, error)
	CurrentOrders(ctx context.Context, structureID int64) ([]store.StoredOrder, error)
	ApplyOrderDiff(ctx context.Context, structureID int64, at time.Time, upserts []store.StoredOrder, deletes []int64, events []store.OrderEvent) error
	RecordRun(ctx context.Context, characterID, structureID int64, at time.Time, status, errMsg string) error
}

// Orders reads the orders of a structure; *esi.Client implements it.
type Orders interface {
	StructureOrders(ctx context.Context, token string, structureID int64) ([]esi.Order, bool, error)
}

// Deps are the collaborators of Collect and Run.
type Deps struct {
	Store  Store
	ESI    Orders
	Tokens auth.TokenSource
	// Now is the clock; it defaults to time.Now.
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// Diff compares the stored orders of a structure with the live ones.
//
//   - an unseen order is a "new" event (delta = volume_remain);
//   - a lower volume_remain is a "fill" event (delta = units filled, positive);
//   - a different price is a "price" event; a volume increase alone is only an
//     update without event;
//   - a stored order missing now is a "gone" event (delta = last known
//     volume_remain); it is cancelled, expired or fully filled and is never
//     counted as a sale on its own.
//
// With no stored orders (first collection) the live orders are stored without
// "new" events, so the baseline does not look like a burst of demand. Only
// changed orders are returned as upserts.
func Diff(prev []store.StoredOrder, now []esi.Order, at time.Time) (upserts []store.StoredOrder, deletes []int64, events []store.OrderEvent) {
	baseline := len(prev) == 0
	old := make(map[int64]store.StoredOrder, len(prev))
	for _, o := range prev {
		old[o.OrderID] = o
	}
	seen := make(map[int64]struct{}, len(now))
	for _, n := range now {
		seen[n.OrderID] = struct{}{}
		cur := store.StoredOrder{
			OrderID: n.OrderID, TypeID: int64(n.TypeID), IsBuy: n.IsBuyOrder, Price: n.Price,
			VolumeRemain: n.VolumeRemain, VolumeTotal: n.VolumeTotal, Issued: n.Issued, Duration: n.Duration,
			FirstSeen: at, LastSeen: at,
		}
		ev := func(kind string, delta int64) store.OrderEvent {
			return store.OrderEvent{OrderID: n.OrderID, TypeID: cur.TypeID, IsBuy: cur.IsBuy, At: at, Kind: kind,
				Price: cur.Price, VolumeRemain: cur.VolumeRemain, DeltaVolume: delta}
		}
		p, ok := old[n.OrderID]
		if !ok {
			upserts = append(upserts, cur)
			if !baseline {
				events = append(events, ev(store.EventNew, n.VolumeRemain))
			}
			continue
		}
		cur.FirstSeen = p.FirstSeen
		changed := p.VolumeRemain != n.VolumeRemain || p.Price != n.Price || p.VolumeTotal != n.VolumeTotal ||
			p.Issued != n.Issued || p.Duration != n.Duration
		if !changed {
			continue
		}
		upserts = append(upserts, cur)
		if n.VolumeRemain < p.VolumeRemain {
			events = append(events, ev(store.EventFill, p.VolumeRemain-n.VolumeRemain))
		}
		if n.Price != p.Price {
			events = append(events, ev(store.EventPrice, 0))
		}
	}
	for _, p := range prev {
		if _, ok := seen[p.OrderID]; ok {
			continue
		}
		deletes = append(deletes, p.OrderID)
		events = append(events, store.OrderEvent{OrderID: p.OrderID, TypeID: p.TypeID, IsBuy: p.IsBuy, At: at,
			Kind: store.EventGone, Price: p.Price, VolumeRemain: p.VolumeRemain, DeltaVolume: p.VolumeRemain})
	}
	return upserts, deletes, events
}

// Collect fetches the orders of one watched structure with its character's
// token, applies the diff and records the outcome. Failures of the fetch are
// recorded as a status and not returned, except a rate limit (*esi.RateLimitError,
// so the caller can back off) and a cancelled context. A failure to store is
// returned.
func Collect(ctx context.Context, d Deps, w store.WatchedStructure) error {
	at := d.now()
	record := func(status, msg string) error {
		if err := d.Store.RecordRun(ctx, w.CharacterID, w.StructureID, at, status, msg); err != nil {
			slog.Error("record collection run", "character_id", w.CharacterID, "structure_id", w.StructureID, "err", err)
			return err
		}
		return nil
	}
	fail := func(err error) error {
		var rl *esi.RateLimitError
		var reauth *auth.ReauthError
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.As(err, &rl):
			slog.Warn("collect structure orders: rate limited", "structure_id", w.StructureID, "retry_after", rl.RetryAfter)
			if rerr := record(StatusRateLimited, msgRateLimited); rerr != nil {
				return errors.Join(err, rerr)
			}
			return err
		case errors.As(err, &reauth):
			return record(StatusNeedsReauth, msgNeedsReauth)
		case esi.IsForbidden(err):
			return record(StatusForbidden, msgForbidden)
		}
		slog.Error("collect structure orders", "character_id", w.CharacterID, "structure_id", w.StructureID, "err", err)
		return record(StatusError, msgError)
	}

	token, err := d.Tokens.Token(ctx, w.CharacterID)
	if err != nil {
		return fail(err)
	}
	live, truncated, err := d.ESI.StructureOrders(ctx, token, w.StructureID)
	if err != nil {
		return fail(err)
	}
	prev, err := d.Store.CurrentOrders(ctx, w.StructureID)
	if err != nil {
		slog.Error("read current orders", "structure_id", w.StructureID, "err", err)
		return fail(err)
	}
	upserts, deletes, events := Diff(prev, live, at)
	status, msg := StatusOK, ""
	if truncated {
		// Partial data: an absent order may simply be on a page we never read.
		deletes = nil
		events = dropGone(events)
		status, msg = StatusTruncated, msgTruncated
	}
	if err := d.Store.ApplyOrderDiff(ctx, w.StructureID, at, upserts, deletes, events); err != nil {
		slog.Error("apply order diff", "structure_id", w.StructureID, "err", err)
		return fail(err)
	}
	return record(status, msg)
}

func dropGone(events []store.OrderEvent) []store.OrderEvent {
	out := events[:0]
	for _, e := range events {
		if e.Kind != store.EventGone {
			out = append(out, e)
		}
	}
	return out
}

// Run collects every watched structure, one at a time. It stops at the first
// rate limit (returning that error so the scheduler can wait Retry-After), on
// context cancellation, and when the watchlist cannot be read.
func Run(ctx context.Context, d Deps) error {
	watched, err := d.Store.ListWatched(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, w := range watched {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := Collect(ctx, d, w)
		var rl *esi.RateLimitError
		if errors.As(err, &rl) || ctx.Err() != nil {
			return err
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
