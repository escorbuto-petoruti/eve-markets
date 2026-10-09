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
- [x] T3 Structure markets: structure discovery (search + universe/structures), structure orders paginated, 403 handling (no docking access), web API + page.

## Route declaration
- T1: delegated (writer); trigger: 2+ non-trivial files, preparation read of eve-wallets.
- T2: delegated (writer), test-first.
- T3: delegated (writer); trigger: 2+ non-trivial files (esi, web, ui, tests).

## Acceptance
- Builds with CGO_ENABLED=0; tests pass; no wallets-specific code left; live sign-in verified once the EVE app is registered (pending owner action).

## Progress / evidence
- Explorer map of eve-wallets completed (2026-10-09).
- T1 done (writer, 2026-10-09). Copied/trimmed: sso (defaults: empty client id, port 8089, MarketScopes), auth, update, esi core (get/postJSON/ETag/errors/UniverseNames), store (fresh single migration: users/tokens/sessions), web skeleton (login/callback/add-character/confirm-move, /api/me, minimal static page), cmd serve/help/version/update, scripts, workflows, install.sh/ps1 (shortcut logic removed), README. internal/scheduler skipped (collection-job oriented, not needed).
- Verification: gofmt -l . empty; go vet ./... clean; go test -count=1 ./... all packages ok; CGO_ENABLED=0 go build ./cmd/eve-markets ok; rg wallet|journal|loyalty|8088 only hits SQLite journal_mode, the origin credit comments, and negative-test fixtures.
- Not run: scripts/test-install-ps1.sh (pwsh not installed locally; CI runs it with REQUIRE_PWSH=1). install.sh --systemd unit has no EVE_MARKETS_CLIENT_ID, so the service would exit with the clear missing-client-id message until the user adds it.
- Part A (before T2): embedded public PKCE client id `cda14518...` as sso.DefaultClientID; EVE_MARKETS_CLIENT_ID still overrides it, blank falls back; serve no longer demands the env var; README updated. Stale text left in install.ps1 (outside the writer's surface): the post-install tail still says to set EVE_MARKETS_CLIENT_ID first.
- T2 done (writer, 2026-10-09). esi.RegionOrders (X-Pages pagination capped at 100, input validation via ErrInvalidInput) and esi.RegionHistory; web GET /api/markets/regions/{region}/orders|history behind requireUser (400 bad input, 429 + Retry-After, 502 generic, 503 without client); Public market panel in the static UI (no new JS libraries); esi client wired in serve with esiUserAgent(). RED: esi and web tests failed to compile (RegionOrders, RegionHistory, ErrInvalidInput, maxMarketPages, Deps.Markets undefined) before implementation. Verification: gofmt -l . empty; go vet ./... clean; go test -count=1 ./... all ok; CGO_ENABLED=0 go build -o /dev/null ./cmd/eve-markets ok. Not run: live ESI, browser check of the UI.
- T3 done (writer, 2026-10-09). esi: SearchStructures, Structure, StructureOrders (shared pagedOrders helper, truncated flag when the 100-page cap stops the loop). web: GET /api/structures/search and /api/structures/{id}/orders behind requireUser; character must belong to the user (404 otherwise), scope check from the stored token, ReauthError -> 401, missing scope -> 403, ESI 403 -> 403 no market access, 429 + Retry-After, 502 generic; upstream errors logged with log/slog (also added to the T2 market handlers); search resolves max 20 structures with 4 workers, 403/404 per structure -> name null. UI: Structure market panel; getJSON helper now shows API error messages in the T2 panel too (R4-004). main.go wires Structures and Tokens. README section added. RED: esi and web test packages failed to compile (SearchStructures, StructureInfo, Deps.Structures undefined) before implementation. Verification: gofmt -l . empty; go vet ./... clean; go test -count=1 ./... all ok; CGO_ENABLED=0 go build -o /dev/null ./cmd/eve-markets ok. NOT verified live: real ESI responses (search, structures, structure orders, 403 behavior), real token refresh/scope check, and the browser UI (no browser run).

## Next step
T3: structure markets (discovery, orders, 403 handling).
- T1 commit: 638065e (feat: scaffold eve-markets from the eve-wallets base). Review assess: high (executable mode, auth, update, process start, shell); consent granted; native START refused with lens_context_budget_exceeded (9636 lines, evidence exceeds budget). Outcome: review pending owner decision (split into smaller commits vs continue unreviewed).
- T2 commit: 8d2a689. Native review (4 lenses, high risk, consent granted): approved and acknowledged; 13 non-blocking advisory findings kept as follow-ups (candidates: upstream error logging in web/markets.go, serial pagination latency and truncation in esi/markets.go, UI error handling in static/app.js:69-89, main.go:213). T1 (638065e) stays unreviewed: native START refused with lens_context_budget_exceeded.

## T3 design notes (2026-10-09)
- ESI has no endpoint listing all market structures. Discovery = search by name (GET /characters/{id}/search/?categories=structure, min 3 chars, scope esi-search.search_structures.v1) plus direct structure id entry; names via GET /universe/structures/{id}/ (403 tolerated per structure); orders via GET /markets/structures/{id}/ (paginated, no type filter, so filter by type_id server-side).
- A character used in the API must belong to the signed-in user; missing scope or ReauthError gives a clear re-login message; ESI 403 means no docking/ACL access.
- Orders responses expose a truncated flag when the page cap is hit (advisory R4-truncation applied to the new code only).
