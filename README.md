# eve-markets

A local web app to explore what the EVE Online ESI offers for markets: public region markets and the private (structure) markets your signed-in characters can reach. It is read-only and discovery-first; this early version signs characters in and shows public region market orders and history (the "Public market" panel).

> eve-markets is not affiliated with or endorsed by CCP Games. EVE Online and the related marks and logos are trademarks of CCP hf.

One process on your machine, one SQLite file, reachable only from loopback (`127.0.0.1`). Characters sign in with EVE SSO (PKCE); several characters can be added to the same session.

## Requirements

- An EVE Online account.
- Nothing to register: the app embeds a public PKCE client id (not a secret). Its redirect is fixed at `http://localhost:8089/auth/callback`, and it asks for the scopes `esi-markets.structure_markets.v1`, `esi-search.search_structures.v1` and `esi-universe.read_structures.v1`.
- Go (version in `go.mod`) only if you build from source; release archives are self-contained.

## Install

Release archives are published on GitHub for linux, macOS and Windows.

```bash
# Linux and macOS: installs to ~/.local/bin, verifies the SHA-256 checksum
curl -fsSL https://raw.githubusercontent.com/escorbuto-petoruti/eve-markets/main/install.sh | sh
```

```powershell
# Windows (PowerShell 5.1 or 7): installs to %LOCALAPPDATA%\eve-markets\bin
irm https://raw.githubusercontent.com/escorbuto-petoruti/eve-markets/main/install.ps1 | iex
```

Or build from source:

```bash
CGO_ENABLED=0 go build ./cmd/eve-markets
```

## Run

```bash
eve-markets serve
```

Then open <http://localhost:8089> and sign in. To use your own EVE application instead, set `EVE_MARKETS_CLIENT_ID=<client id>` (a blank value is ignored); it must register the callback `http://localhost:8089/auth/callback`. Run `eve-markets` with no arguments to print the help.

| Command | Purpose |
|---------|---------|
| `serve [--addr 127.0.0.1:8089] [--db PATH] [--open \| --no-open]` | serve the page on a loopback address |
| `update [--check] [--force]` | replace the binary with the latest release (linux and macOS) |
| `version` | print the version |

Environment: `EVE_MARKETS_CLIENT_ID` (optional override of the embedded client id), `EVE_MARKETS_DB` (database path; the `--db` flag wins). The redirect URL is fixed to port 8089, so keep the default `--addr` port or SSO sign-in will not work.

## Structure markets

The page has a Structure market panel for player-structure (private) markets.
ESI cannot list every market structure, so you either search by name (3
characters or more, needs the `esi-search.search_structures.v1` scope) or type
a structure id. Orders come from `GET /markets/structures/{id}/`, filtered by
type on the server; a large market may be cut at 100 pages and is then flagged
as truncated.

The structure search lists the structures where the character has docking
permission. Not all of them have a market, so each result carries a badge: the
app probes `GET /markets/structures/{id}/?page=1` (at most 20 per search, 4 at
a time, one orders page each) and shows `yes` (200), `no_market` ("No market",
403 or 404: ESI refused the market read, most likely because the structure has
no market service installed) or `unknown`. Structures with a market are listed
first, then unknown, then no market; "Only with a market" hides the rest.
Definitive answers are remembered per character for 24 hours; "Re-check
markets" re-probes ignoring that memory. `unknown` means the check failed
(network or ESI error) or ESI rate limited the probes, so the remaining
results were not checked; it is never remembered, so searching again or
Re-check markets later retries it.

ESI does not document what its 403 and 404 mean here, so the app records what
it answered: the search results carry `market_status` (the HTTP status, 0 when
unknown) and `market_detail` (the `error` text of ESI's body, at most 200
printable characters), the latter shown as a tooltip on the "No market" badge.
Answers cached before this was recorded have neither.

A `403 ESI refused the market read for this structure` on the orders view has
the same meaning: most likely the structure has no market service, or the
character has no market access. If a character was added before the market
scopes existed, re-add it to grant them.

## Collecting watched structures

ESI only shows the current orders of a structure, never history. To study what
is demanded in a private market, press **Watch** on a search result that has
market access. While `eve-markets serve` runs it fetches the orders of every
watched structure on a timer, one structure at a time, with the token of the
character that watches it.

```bash
eve-markets serve --collect-interval 15m   # default; minimum 1m, 0 disables
```

Per structure the database keeps the current open orders (`orders_current`)
and an event log of changes between two collections (`order_events`), not full
snapshots. The first collection of a structure only stores the baseline and
emits no events. Event kinds:

- `new`: an order appeared (delta = its remaining volume)
- `fill`: the remaining volume dropped (delta = units filled)
- `price`: the price changed (a volume increase alone is just an update)
- `gone`: the order vanished (cancelled, expired or fully filled, which cannot
  be told apart; it is never counted as a sale on its own)

Each watch shows its last status: `ok`, `truncated` (the page cap was reached,
so nothing is marked `gone`), `forbidden` (ESI refused the market read), `needs_reauth`
(sign that character in again), `rate_limited` or `error`. After a rate limit
the collector waits the `Retry-After` before the next run. Unwatching stops the
collection and keeps what was stored.

## Demand

Under **Watched structures** the **Demand** panel summarizes what members
buy and sell in a watched structure over the last 1, 7 or 30 days
(`GET /api/demand?structure_id=&days=&limit=&sort=`, days 1-90, limit up to 500,
sort by `sold_value`, `sold_units`, `open_buy_value` or `bought_value`). The
structure must be on the watchlist of one of your characters.

- **Sold units / value**: units that left sell orders (`fill` events on sell
  orders, i.e. bought by members), valued at the order price of the event.
- **Bought units / value**: units that were sold into buy orders.
- **Open buy volume / value**: what the open buy orders ask for right now, so
  an item with demand but no fills yet still shows up. **Best sell** is the
  lowest sell price and **Best buy** the highest buy price (`-` when none).

These are inferences, not trades. The collector only compares snapshots every
`--collect-interval`, so several fills of one order between two runs add up to
one event at the later price, and an order that appears and vanishes between
two runs is never seen. `gone` orders are never counted as sales (they may
have been cancelled or expired). Data starts at the first collection of the
structure (the baseline emits no events), shown in the panel as "Collecting
since"; fills appear from the second run.

### Jita comparison

Tick **Compare with Jita** (or add `jita=1&jita_limit=N` to `/api/demand`,
N 1-50, default 20) to compare prices with Jita IV - Moon 4 (The Forge). Only
the first N rows of the chosen sort are compared; the others show `-`.

- **Jita sell / buy**: lowest sell and highest buy price at the Jita station
  (orders of other Forge stations are ignored). Each row also carries
  `sell_diff`, `sell_pct`, `buy_diff` and `buy_pct` in its `jita` object.
- **Sell vs Jita / Buy vs Jita**: sell is compared with sell and buy with buy,
  as `(alliance - Jita) / Jita`. A positive value means the alliance is more
  expensive. It is `-` when either side has no order.
- Jita prices come from one ESI request per type (4 at a time) and are cached
  in memory for 5 minutes. If ESI fails for some types their rows show `-` and
  `meta.jita.status` is `partial`; if it fails for all (or the market client is
  not configured) the status is `unavailable` and the demand view still loads.
  The comparison is off unless requested.

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
```

Scaffolded from [eve-wallets](https://github.com/escorbuto-petoruti/eve-wallets); released under the license in `LICENSE`.
