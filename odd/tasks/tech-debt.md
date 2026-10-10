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
- [x] D4 Error-path tests. Commit efea17c.
- [x] D5 JS helpers extracted and tested; CI step for `node --test`. Commit a2e2657.

## Route declaration
- Each task: delegated writer, one at a time. Trigger: 2+ non-trivial files (D1 web.go+jita.go+test, D2 store+web+tests, D3 collector+tests, D4 tests across 3 packages, D5 js+html+CI).
- Delivery: one branch `chore/tech-debt`, one work-unit commit per task, single PR; forecast ~500 changed lines, so `ask-on-risk` applies if the running count passes 400 before the last commit.

## Acceptance
gofmt/vet/test green; `go test -race -count=2` on touched packages; CGO_ENABLED=0 build; `node --check` and `node --test` for D5; no API response shape changes.

## Progress / evidence
- D1 (writer): `jitaCaches`/`quoteCache()` removed; `server.quotes` built in `New` with `newQuoteCache(quoteTTL)`; `quoteCache.get/put` are nil-safe (a bare `&server{}` fetches uncached). Test `TestDemandJitaCacheIsPerServer` added. RED exception: the old map was keyed by `*server`, so the test already passed before the change (it guards the behavior). Not verified by the parent yet.
- D2 (writer): `DemandSummarySorted` reads rows and meta in one `ReadOnly` transaction (driver accepts it); rows cursor closed before the meta query. `store.MaxDemandLimit = 500` clamps the limit; `web/demand.go` references it. RED: `TestDemandSummaryClampsLimit` failed to compile (`MaxDemandLimit` undefined), GREEN after the change. Same-snapshot behavior is not covered by a deterministic test. Not verified by the parent yet.
- D3 (writer): `Run` groups adjacent watches by structure and calls the new `collectStructure`, which fetches/diffs/applies once using the lowest character's token. Reauth or forbidden records that watcher and falls through to the next; rate limit and other errors stop and are recorded for the failing and untried watchers; success is recorded for every served watcher. `Collect` keeps its signature (wraps a single-watcher group); scheduler untouched (it only receives `collector.Run` from main). README does not document the one-fetch-per-watch behavior, unchanged. RED: four new `Run` tests failed (extra fetches/records) before the change; GREEN after. Not verified by the parent yet.
- D5 (writer): pure `fmt*` helpers moved to `static/format.js` (plain script exposing `window.EveFormat`, CommonJS export under node); `index.html` loads it before `app.js`, which aliases the helpers (output unchanged). `web.go` embed list and `staticTypes` gained `format.js`; `web_test.go` static-asset tests cover it. Tests in `internal/web/jstest/format.test.js` (node:test, en-US formatters are pinned in the helper; `fmtTime` is compared with the same `toLocaleString` call because it is locale and time zone dependent). RED: `Cannot find module '../static/format.js'`; GREEN after extraction. CI gained setup-node (v7.1.0 pinned by SHA), `node --check` for both files and `node --test internal/web/jstest/*.test.js` (a directory argument fails on Node 24, so the glob is used). README Development lists the node commands. Not verified by the parent yet.
- D4 (writer): tests only, no production change. Web: `TestDemandStoreFailuresAre500` makes `ListWatchedForUser` and `DemandSummarySorted` fail by renaming `watched_structures` / `order_events` through a second connection (the store is a concrete type, so no fake is possible); `jita=-1` and `jita_limit=-1` added to `TestDemandJitaValidation` (the other `jita`/`jita_limit` 400 cases already existed there). Collector: fakeStore gained `listErr`, `curErr`, `runErr`; new tests cover `CurrentOrders` and `ApplyOrderDiff` failures, `RecordRun` failures (single, per-watcher, rate limit joined with the record error) and `Run` returning the `ListWatched` error and the first error. ESI: `TestSanitizeDetail` unit table (the 404 detail and the long/control case were already covered by `TestProbeStructureMarket`). Observation: `Collect` returns nil when `CurrentOrders`/`ApplyOrderDiff` fail and the error status is recorded, although its doc comment says a failure to store is returned; the tests pin the current behavior. RED exception: characterization tests of existing behavior. Not verified by the parent yet.

## Next step
Run the native review on the whole slice (main..HEAD, committed only), then open the PR once the owner decides.

- Commits: D1 59272c0, D2 c318e85 (+ fix 498e9ab), D3 9ec9e46, D5 a2e2657, D4 efea17c.
- First review (D1-D3, 401 lines, medium): the reliability lens found a real CRITICAL bug in D2 (`meta` query ran on `s.db` instead of the read-only transaction, `store/demand.go:157`). Fixed in 498e9ab. The lineage then stopped with `captured_artifacts_unverifiable`, most likely because untracked files were moved and a commit landed while the review was open. It was released with `review abandon` (owner-authorized, `operator_disposition`); the slice is re-reviewed as a fresh candidate. Lesson: no commits or file moves while a review is open.
- Known behavior left as is: `Collect` returns nil on `CurrentOrders`/`ApplyOrderDiff` failures when `RecordRun` succeeds (its doc comment says otherwise). Fix the comment or the behavior in a later task.
- Not verified live: dedupe with two real watchers on one structure, CI run of the new node step.
