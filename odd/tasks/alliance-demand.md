# Feature: alliance-demand

## Objective
Understand what is demanded in the alliance market (a private structure) and how its prices differ from Jita (the public hub).

## Why
ESI exposes only current orders of a structure, no history. Demand must be inferred from open buy orders and from `volume_remain` deltas between snapshots. Owner confirmed (2026-10-10): hub comparison starts with Jita only, hub list kept configurable.

## Scope
- Watchlist of structures (per character that can open them), collected periodically inside `serve`.
- Compact storage: current order state plus an event log of changes (not full snapshots; an alliance structure can hold tens of thousands of orders).
- Demand view (what sells / is requested in the alliance).
- Jita comparison (price differential per type).
- Out of scope: other hubs, alerts, charts beyond tables, multi-structure aggregation, Windows double-click.

## Defaults decided by the assistant (changeable)
- Collection interval 15 min (flag/env), honoring ESI cache expiry; backoff on 429.
- Events kept 90 days (pruning is a later task).
- Jita = The Forge region 10000002, station 60003760 (Jita IV - Moon 4 - Caldari Navy Assembly Plant); hubs live in a small config table in code.
- Inference rules: `fill` = volume_remain decreased; `gone` = order vanished (cancelled, expired or fully filled: ambiguous, kept separate, never counted as sales on its own); `new`; `price` change.

## Tasks
- [ ] T1 Watchlist + collector: tables `watched_structures`, `orders_current`, `order_events` (migration 3), diffing collector, loop in `serve`, per-structure status (last run, error, needs re-login), API to watch/unwatch/list, minimal UI. Test-first.
- [ ] T2 Demand view: per type over a window (default 7d): units filled, value, open buy volume/value, last price; API + table UI.
- [ ] T3 Jita comparison: on-demand Jita orders per type (region orders filtered by type_id and location_id, TTL cache, bounded to the top N types of the demand view), best sell/buy vs the alliance, absolute and % differential; API + UI columns.

## Route declaration
- T1, T2, T3: delegated writers, one at a time; trigger: 2+ non-trivial files each (store migration, collector, web, ui, tests).

## Acceptance
gofmt/vet/test green; CGO_ENABLED=0 build; migrations applied on an existing DB without data loss; live check by owner after each task.

## Progress / evidence
(none yet)

## Next step
Launch the T1 writer.
