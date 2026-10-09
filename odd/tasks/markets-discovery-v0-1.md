# Feature: markets-discovery-v0-1

## Objective
Local Go server (loopback, SQLite, EVE SSO PKCE, multi-character) that shows what ESI offers for markets: public region markets and private (structure) markets reachable by the signed-in characters. Read-only, discovery-first.

## Why
Direction of the project is open. Learn what ESI returns for public and alliance-internal markets before designing snapshots or analysis.

## Scope (authorized by owner: "arrancamos con ese alcance")
- Reuse the eve-wallets base (sso, auth, esi core, store users/tokens/sessions, web skeleton, release tooling).
- Public: region orders and history per type.
- Private: list market-capable structures per character, show their orders.
- Out of scope: snapshots, charts, wallets, Windows double-click (no-args serve, Quit button, shutdown endpoint, shortcuts).

## Constraints
- Module `github.com/escorbuto-petoruti/eve-markets`, identity escorbuto-petoruti, conventional commits, no AI attribution.
- Redirect `http://localhost:8089/auth/callback` (8088 belongs to eve-wallets). Client id via `EVE_MARKETS_CLIENT_ID`, never hardcoded until the owner registers the EVE app.
- Scopes: esi-markets.structure_markets.v1, esi-search.search_structures.v1, esi-universe.read_structures.v1.
- Planning heuristic ~400 authored changed lines per task; copied code counts as a single scaffold unit.

## Tasks
- [x] T1 Scaffold from eve-wallets: module, sso/auth/update, esi core (get, cache, errors), store (users/tokens/sessions, fresh migrations), web skeleton + login/add-character, cmd serve, scripts, workflows, LICENSE, README. Check: `go vet`, `go test ./...` green.
- [x] T2 Public markets: esi region orders (paginated via X-Pages) and history, web API + minimal page. Test-first with httptest fakes.
- [ ] T3 Structure markets: structure discovery (search + universe/structures), structure orders paginated, 403 handling (no docking access), web API + page.

## Route declaration
- T1: delegated (writer); trigger: 2+ non-trivial files, preparation read of eve-wallets.
- T2, T3: decided when reached.

## Acceptance
- Builds with CGO_ENABLED=0; tests pass; no wallets-specific code left; live sign-in verified once the EVE app is registered (pending owner action).

## Progress / evidence
- Explorer map of eve-wallets completed (2026-10-09).
- T1 done (writer, 2026-10-09). Copied/trimmed: sso (defaults: empty client id, port 8089, MarketScopes), auth, update, esi core (get/postJSON/ETag/errors/UniverseNames), store (fresh single migration: users/tokens/sessions), web skeleton (login/callback/add-character/confirm-move, /api/me, minimal static page), cmd serve/help/version/update, scripts, workflows, install.sh/ps1 (shortcut logic removed), README. internal/scheduler skipped (collection-job oriented, not needed).
- Verification: gofmt -l . empty; go vet ./... clean; go test -count=1 ./... all packages ok; CGO_ENABLED=0 go build ./cmd/eve-markets ok; rg wallet|journal|loyalty|8088 only hits SQLite journal_mode, the origin credit comments, and negative-test fixtures.
- Not run: scripts/test-install-ps1.sh (pwsh not installed locally; CI runs it with REQUIRE_PWSH=1). install.sh --systemd unit has no EVE_MARKETS_CLIENT_ID, so the service would exit with the clear missing-client-id message until the user adds it.
- Part A (before T2): embedded public PKCE client id `cda14518...` as sso.DefaultClientID; EVE_MARKETS_CLIENT_ID still overrides it, blank falls back; serve no longer demands the env var; README updated. Stale text left in install.ps1 (outside the writer's surface): the post-install tail still says to set EVE_MARKETS_CLIENT_ID first.
- T2 done (writer, 2026-10-09). esi.RegionOrders (X-Pages pagination capped at 100, input validation via ErrInvalidInput) and esi.RegionHistory; web GET /api/markets/regions/{region}/orders|history behind requireUser (400 bad input, 429 + Retry-After, 502 generic, 503 without client); Public market panel in the static UI (no new JS libraries); esi client wired in serve with esiUserAgent(). RED: esi and web tests failed to compile (RegionOrders, RegionHistory, ErrInvalidInput, maxMarketPages, Deps.Markets undefined) before implementation. Verification: gofmt -l . empty; go vet ./... clean; go test -count=1 ./... all ok; CGO_ENABLED=0 go build -o /dev/null ./cmd/eve-markets ok. Not run: live ESI, browser check of the UI.

## Next step
T3: structure markets (discovery, orders, 403 handling).
- T1 commit: 638065e (feat: scaffold eve-markets from the eve-wallets base). Review assess: high (executable mode, auth, update, process start, shell); consent granted; native START refused with lens_context_budget_exceeded (9636 lines, evidence exceeds budget). Outcome: review pending owner decision (split into smaller commits vs continue unreviewed).
