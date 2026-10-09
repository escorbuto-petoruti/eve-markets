# eve-markets

A local web app to explore what the EVE Online ESI offers for markets: public region markets and the private (structure) markets your signed-in characters can reach. It is read-only and discovery-first; this early version only signs characters in.

> eve-markets is not affiliated with or endorsed by CCP Games. EVE Online and the related marks and logos are trademarks of CCP hf.

One process on your machine, one SQLite file, reachable only from loopback (`127.0.0.1`). Characters sign in with EVE SSO (PKCE); several characters can be added to the same session.

## Requirements

- An EVE Online account.
- Your own EVE application registration, because this project ships no client id. Create one at the [EVE developer portal](https://developers.eveonline.com/) as an authentication and API access application with:
  - Callback URL: `http://localhost:8089/auth/callback`
  - Scopes: `esi-markets.structure_markets.v1`, `esi-search.search_structures.v1`, `esi-universe.read_structures.v1`
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
export EVE_MARKETS_CLIENT_ID=<your application's client id>
eve-markets serve
```

Then open <http://localhost:8089> and sign in. `serve` refuses to start when `EVE_MARKETS_CLIENT_ID` is empty. Run `eve-markets` with no arguments to print the help.

| Command | Purpose |
|---------|---------|
| `serve [--addr 127.0.0.1:8089] [--db PATH] [--open \| --no-open]` | serve the page on a loopback address |
| `update [--check] [--force]` | replace the binary with the latest release (linux and macOS) |
| `version` | print the version |

Environment: `EVE_MARKETS_CLIENT_ID` (required for `serve`), `EVE_MARKETS_DB` (database path; the `--db` flag wins). The redirect URL is fixed to port 8089, so keep the default `--addr` port or SSO sign-in will not work.

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
```

Scaffolded from [eve-wallets](https://github.com/escorbuto-petoruti/eve-wallets); released under the license in `LICENSE`.
