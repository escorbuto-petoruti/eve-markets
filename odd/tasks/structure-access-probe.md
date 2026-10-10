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
- [ ] T1 Probe + cache + API + UI, test-first (esi probe, store migration 2, web search handler, static UI).

## Route declaration
- T1: delegated (writer); trigger: 2+ non-trivial files (esi, store migration, web, ui, tests).

## Acceptance
gofmt/vet/test green; CGO_ENABLED=0 build; migration applied on an existing DB without data loss; live check by owner.

## Progress / evidence
(none yet)

## Next step
Launch the T1 writer.
