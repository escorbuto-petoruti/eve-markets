# Feature: tech-debt

## Objective
Pay down the advisories left by the alliance-demand reviews (T1 to T3) without changing user-visible behavior.

## Why
Non-blocking review advisories accumulated: duplicate ESI fetches, a non-transactional read, a global cache, untested error paths and untested UI helpers.

## Scope
- D1 Move the Jita quote cache from the package-level `jitaCaches` into a `server` field (`web.go`, `jita.go`).
- D2 Store: read `rows` and `meta` of `DemandSummarySorted` inside one read transaction; clamp `limit` in the store (constant shared with the web layer).
- D3 Collector: fetch each structure once per run even if several characters watch it; try the next watcher's token on reauth/forbidden; `RecordRun` for every watch.
- D4 Tests for untested error paths: `jita`/`jita_limit` 400s, 500 paths in `web/demand.go`, `collector.Collect` store failures, `esi.sanitizeDetail`.
- D5 Extract `fmt*` helpers into `static/format.js` and cover them with a dependency-free `node --test` suite kept outside the embedded `static/` dir.
- Out of scope: new features, schema changes, ACL-restricted market behavior, pruning.

## Defaults decided by the assistant (changeable)
- D3: prefer the watcher with the lowest `character_id` (ListWatched order); on `ReauthError` or forbidden fall through to the next watcher; the final failure is recorded only for the watchers that actually failed, success is recorded for every watcher of the structure that was served.
- D2: the store clamps `limit` to the existing cap of 500 (no error); `jita=2` and similar stay a 400.
- D5: tests live in `internal/web/jstest/`, not under `static/`.

## Tasks
- [x] D1 Quote cache into `server` field. Test-first where a failing test is meaningful (cache per server instance). Commit 59272c0.
- [x] D2 Read-only transaction for demand summary + clamp limit.
- [x] D3 Collector dedupe per structure. Commit 9ec9e46.
- [ ] D4 Error-path tests.
- [ ] D5 JS helpers extracted and tested; CI step for `node --test`.

## Route declaration
- Each task: delegated writer, one at a time. Trigger: 2+ non-trivial files (D1 web.go+jita.go+test, D2 store+web+tests, D3 collector+tests, D4 tests across 3 packages, D5 js+html+CI).
- Delivery: one branch `chore/tech-debt`, one work-unit commit per task, single PR; forecast ~500 changed lines, so `ask-on-risk` applies if the running count passes 400 before the last commit.

## Acceptance
gofmt/vet/test green; `go test -race -count=2` on touched packages; CGO_ENABLED=0 build; `node --check` and `node --test` for D5; no API response shape changes.

## Progress / evidence
- D1 (writer): `jitaCaches`/`quoteCache()` removed; `server.quotes` built in `New` with `newQuoteCache(quoteTTL)`; `quoteCache.get/put` are nil-safe (a bare `&server{}` fetches uncached). Test `TestDemandJitaCacheIsPerServer` added. RED exception: the old map was keyed by `*server`, so the test already passed before the change (it guards the behavior). Not verified by the parent yet.
- D2 (writer): `DemandSummarySorted` reads rows and meta in one `ReadOnly` transaction (driver accepts it); rows cursor closed before the meta query. `store.MaxDemandLimit = 500` clamps the limit; `web/demand.go` references it. RED: `TestDemandSummaryClampsLimit` failed to compile (`MaxDemandLimit` undefined), GREEN after the change. Same-snapshot behavior is not covered by a deterministic test. Not verified by the parent yet.
- D3 (writer): `Run` groups adjacent watches by structure and calls the new `collectStructure`, which fetches/diffs/applies once using the lowest character's token. Reauth or forbidden records that watcher and falls through to the next; rate limit and other errors stop and are recorded for the failing and untried watchers; success is recorded for every served watcher. `Collect` keeps its signature (wraps a single-watcher group); scheduler untouched (it only receives `collector.Run` from main). README does not document the one-fetch-per-watch behavior, unchanged. RED: four new `Run` tests failed (extra fetches/records) before the change; GREEN after. Not verified by the parent yet.

## Next step
Review the D1-D3 slice (assessed high risk), then launch the D4 writer. (D1 commit 59272c0 and D2 commit c318e85 verified by the parent: gofmt clean, web and store tests green. Review assessment is deferred until the slice reaches the delivery budget.)
