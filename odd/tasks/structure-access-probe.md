# Feature: structure-access-probe

## Objective
In the structure search, mark which structures the selected character can actually open the market of, so the user stops probing blindly.

## Why
Live test: a name in search results does not imply market access (random structure returned 403, alliance structure worked). The only reliable signal is the market endpoint itself.

## Scope (owner: "arrancamos por ahi")
- Probe `GET /markets/structures/{id}/?page=1` per search result: 200 = yes, 403 = no, anything else = unknown (not cached).
- Cache per (character, structure) in SQLite with a TTL (24h); `refresh=1` bypasses it.
- Search results carry `market_access: yes|no|unknown`; replace the ambiguous `accessible` field (which only meant "name resolved").
- UI: badges, accessible first, optional "only accessible" filter.
- Out of scope: snapshots/history, price analysis, probing structures not returned by the search.

## Constraints
- Bounded: at most 20 results probed, concurrency 4; stop probing on first 429 and mark the rest unknown.
- Never cache transient errors. Never log tokens. Artifacts in English; conventional commits, no AI attribution.
- Probe cost: one orders page per structure (up to ~1000 orders), hence the cache.

## Tasks
- [x] T1 Probe + cache + API + UI, test-first (esi probe, store migration 2, web search handler, static UI).

## Route declaration
- T1: delegated (writer); trigger: 2+ non-trivial files (esi, store migration, web, ui, tests).

## Acceptance
gofmt/vet/test green; CGO_ENABLED=0 build; migration applied on an existing DB without data loss; live check by owner.

## Progress / evidence
- T1 (writer, uncommitted): RED observed first (esi, store and web test packages failed to compile: undefined ProbeResult/ProbeYes/ProbeNo, Get/SaveStructureAccess, esi.ProbeResult in the fake). GREEN: gofmt -l empty; go vet ./... clean; go test -count=1 ./... all ok (web also -race -count=3); CGO_ENABLED=0 go build ok; node --check app.js ok.
  - Added: esi.ProbeStructureMarket, store migration 2 (structure_access, FK to tokens ON DELETE CASCADE) + Get/SaveStructureAccess, migration 1->2 no-data-loss test, search handler `market_access` (cache 24h via injectable Deps.Now, refresh=1, 4 workers, 20 cap, stop on first rate limit, Retry-After header), UI badges/filter/refresh, README.
  - Not verified live: real ESI responses, rate-limit behaviour, browser rendering of the UI (owner to test).

## Next step
Parent commits T1; owner runs the live check.
- T1 commit: 00d247e. Review: medium (688 lines), consent granted, reliability lens approved and acknowledged. Advisories (non-blocking follow-ups): web/structures.go:201-213, 203-204, 222-233, 252-254 (probe/cache edge paths) and store/store.go:49. Not verified live: ESI probe behavior, UI.
