# Feature: demand-share

## Objective
Show the total ISK sold in the window and each item's share of it in the Demand table.

## Why
The ranking by sold value lacks scale: without the window total, a 9.6 B row cannot be read as a share of the market.

## Scope
- S1 Store: `DemandMeta.TotalSoldValue`, the sum of `delta_volume * price` over ALL sell-side fill events of the structure in the window (not only the returned top rows), computed inside the existing read transaction. Test-first.
- S2 API: `meta.total_sold_value` in `/api/demand`; tests.
- S3 UI: a "Share" column (`sold_value / total_sold_value`, one decimal, `-` when the total is 0) and the total shown in the status line; share math in `static/format.js` with node tests; README note.
- Out of scope: schema changes, new endpoints, bought-side totals.

## Defaults decided by the assistant (changeable)
- Share is of total sold value (sell-order fills), regardless of the chosen sort or limit.
- Percentage is computed client-side from `meta.total_sold_value`; the API rows are unchanged.

## Tasks
- [x] S1 Store total sold value (test-first). Commit 399b1b3. RED: test failed to compile (`meta.TotalSoldValue undefined`), GREEN after the query change.
- [x] S2 API meta field + tests. Commit a0a0caf.
- [x] S3 UI column, status total, format.js helper + node tests, README. Commit 050f945.

## Verification
- `go build ./...`, `go vet ./...`, `go test ./...`: pass. `node --test internal/web/jstest/*.test.js`: 10 pass, 0 fail.

## Route declaration
- One delegated writer for S1-S3. Trigger: 2+ non-trivial files (store, web, static JS, tests).

## Delivery
- Strategy: ask-on-risk; forecast ~120 authored lines, single slice, below 400.
