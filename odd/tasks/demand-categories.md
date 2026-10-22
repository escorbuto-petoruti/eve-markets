# Feature: demand-categories

Group the Demand table by item category with per-item detail inside each group, and show hauling volume (m³).
Branch: feat/demand-categories.

## Specs
- S1 "es importante agrupar en categorías, pero también tener el detalle de que item de esa categoría se está consumiendo" — demand rows carry their category (and group); the UI groups by category and each category expands to its items.
- S2 "Otro dato importante es el volumen de esos productos. Porque la logística en este juego es importante y el volumen en metros cúbicos influye en el costo de transporte" — each item shows unit volume (m³) and sold volume (sold units × unit m³); each category shows the sum; ISK per m³ is shown so transport cost can be weighed.
- S3 Type metadata (name, group, category, volume) comes from public ESI `/universe/types`, `/universe/groups`, `/universe/categories`, cached in SQLite (new additive table) and filled lazily; an ESI failure never breaks `/api/demand` (rows just lack metadata).
- S4 Existing behavior unchanged: the flat table, sorts, share, Jita comparison keep working; help text and README describe the new view.

## Defaults decided by the assistant (changeable)
- Hauling volume uses `packaged_volume` when ESI has it, else `volume` (ships are hauled packaged).
- Category rows are sorted by sold ISK; items inside keep the chosen sort.
- A "Group by category" checkbox (on by default) switches between grouped and flat view.

## Tasks
- [x] T1 S3 ESI client: type, group, category lookups (test-first). Commit f91fefe.
- [x] T2 S3 Store: `types` cache table + migration + upsert/get (test-first). Risk: schema addition. Verified on a copy of the live DB: v4 to v5, 192139 events kept, bak-v4 written.
- [x] T3 S1,S2 API: rows gain `category`, `group`, `unit_volume`, `sold_volume`; lazy fill (test-first). go test ./... and -race on web pass.
- [ ] T4 S1,S2,S4 UI: grouped view, m³ and ISK/m³ columns, format.js helpers + node tests, README.

## Log
- L1 (user, 2026-10-22) "podriamos hacer algo para mejorar la visualización de que se está consumiendo en los mercados privados"
- L2 (user) "es importante agrupar en categorías, pero también tener el detalle de que item de esa categoría se está consumiendo. Otro dato importante es el volumen de esos productos. Porque la logística en este juego es importante y el volumen en metros cúbicos influye en el costo de transporte"
