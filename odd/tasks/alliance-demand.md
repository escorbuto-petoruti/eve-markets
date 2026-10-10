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
- [x] T1 Watchlist + collector: tables `watched_structures`, `orders_current`, `order_events` (migration 3), diffing collector, loop in `serve`, per-structure status (last run, error, needs re-login), API to watch/unwatch/list, minimal UI. Test-first.
- [x] T2 Demand view: per type over a window (default 7d): units filled, value, open buy volume/value, last price; API + table UI.
- [ ] T3 Jita comparison: on-demand Jita orders per type (region orders filtered by type_id and location_id, TTL cache, bounded to the top N types of the demand view), best sell/buy vs the alliance, absolute and % differential; API + UI columns.

## Route declaration
- T1, T2, T3: delegated writers, one at a time; trigger: 2+ non-trivial files each (store migration, collector, web, ui, tests).

## Acceptance
gofmt/vet/test green; CGO_ENABLED=0 build; migrations applied on an existing DB without data loss; live check by owner after each task.

## Progress / evidence
- T1 (writer, delegated): RED observed (store/collector/scheduler/web tests failed to compile: undefined AddWatch, store.StoredOrder, Diff, Loop). GREEN: gofmt clean; go vet ./...; go test -count=1 ./...; go test -race -count=2 ./internal/collector ./internal/scheduler ./internal/web; CGO_ENABLED=0 go build; node --check app.js, all pass.
  - Design notes: migration 3 appended; `Diff` takes `[]esi.Order` (the ESI type); first collection (no stored rows) stores a baseline without events; truncated fetch drops `gone` events/deletes and records status `truncated`; `last_seen` only changes when an order row changes (unchanged orders are not rewritten); `fill` and `price` can both fire for one order; a structure watched by two characters is fetched once per watch (no dedupe yet); the usage-text test now forbids "eve-markets collect" instead of the word "collect".
  - Not verified live: real ESI collection, browser UI (Watch/Unwatch buttons), a 429 backoff against ESI, migration on a real user database.
  - Commit: pending (parent commits).

- T1b: no_market wording + probe status/detail recorded (migration 4). Owner clarified that search lists structures the character can dock at, so `no` became `no_market` (ESI refused the market read, most likely no market service). Probe now returns `esi.Probe{Result,Status,Detail}`; `structure_access` gained nullable `http_status`/`detail`; search results carry `market_status`/`market_detail` (tooltip on the badge). RED observed (esi, store, web tests failed to compile on the missing Probe/StructureAccess types). Not verified live: real ESI 403/404 error bodies (what `error` text ESI sends), the browser tooltip, migration 4 on a real user database.

- T2 (writer, delegated): RED observed (store tests failed to compile on the missing DemandSummary/DemandRow/DemandSort*; web tests all returned 404 for /api/demand). GREEN: gofmt clean; go vet ./...; go test -count=1 ./...; go test -race -count=2 ./internal/web ./internal/store; CGO_ENABLED=0 go build; node --check app.js, all pass.
  - Design notes: `DemandSummary` (sold value order) wraps `DemandSummarySorted` with a whitelisted sort key (needed so the cap applies after the requested sort); window is inclusive on both ends (since <= at <= until); only `fill` events count; `FirstCollectedAt` = min(orders_current.first_seen, order_events.at), a lower bound of the baseline (the watch's created_at predates the first collection and is not used); `LastRunAt` = latest last_run_at over watches of the structure; no new index (existing (structure_id, type_id, at) and (structure_id, type_id) cover the GROUP BYs). `/api/demand` answers 404 "structure not found" for unwatched and foreign structures alike; type names are optional.
  - Not verified live: real collected data in the UI, browser rendering of the Demand panel (layout, number formatting), query time on a large (tens of thousands of orders, 90 days of events) database.
  - Commit: pending (parent commits).

## Next step
Review/commit T1, then launch the T2 writer.
- T1 commit: 63270d3. Review: medium (1629 lines), consent granted, reliability lens approved and acknowledged. Advisories (non-blocking): collector.go:143-162 and 85-117 (run/collect edge paths), collector_test.go:106-140 (coverage), README.md:100-101. Known deviations: a structure watched by two characters is fetched once per watch (no dedupe); last_seen only changes when a row changes. Not verified live: real ESI collection, 429 backoff, migration 3 on a real DB.
- T1b commit: 4d38861 (no_market wording, probe status/detail recorded, migration 4). Review: medium (428 lines), consent granted, reliability lens approved and acknowledged. Advisories: esi/structures.go:126-127 and 136-150 (probe detail edge cases). Open question to verify live: what ESI returns (status/detail) for structures without a market service.
- T2 commit: 0192383. Review: medium (819 lines), consent granted, reliability lens approved and acknowledged. Advisories (non-blocking): store/demand.go:81-83 (limit not bounded in the store itself), 84-147 (rows and meta are read in separate non-transactional queries, so they can disagree under concurrent collection), 116-150 and web/demand.go:96-122 (error paths untested). Not verified live: Demand panel in the browser, a real collected DB, query time on a large DB.
