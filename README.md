# Fic Tally — Reading Tracker

A lightweight, self-hosted manga/novel reading tracker. Single user, no
accounts, no forums, no release feeds. Implements the spec at
`reading-tracker-spec-rev1.md` (kept in the project upload folder, not
included here for brevity).

> Formerly named **Tsundoku** — renamed to Fic Tally. An existing
> `tsundoku.db` is picked up automatically on first run (nothing is renamed
> behind your back); new databases are `fic-tally.db`.

## What's inside

- **Library** — cover grid with the ribbon progress indicator, search
  (matches titles, **alternative titles**, author, tags, description;
  **multi-term** queries and `#tag` tokens like `#isekai #romance`),
  filter by status/type and **multiple tags at once**
  (`?tag=isekai,romance` — AND semantics — with removable filter
  chips), sort by last read / title / rating / **updated (the
  default)**; three **layout modes** (default / compact / details).
- **Saved views ("shelves")** — pin any filter+sort combination as a
  named shortcut ("Reading now", "Seasonal cull") from the
  *Save view…* popover on the library page; shelves are stored
  server-side, and the chip matching your current view lights up.
- **Bulk status changes** — hidden behind a **Select multiple** toggle in
  the viewbar: click it and every card grows a checkbox (placed outside
  the card link, so ticking never opens the series); pick a status and
  apply it to the whole selection in one POST. The reveal is pure CSS
  (`:has()` on a nameless checkbox) — JS only live-updates the
  "N selected" hint, and the mode resets when you navigate away.
- **Progress tracking** — chapter number + label (handles "Extra 1",
  "Vol. 4 Ch. 2"), quick +1, bookmark as a labeled resume point, 1-10
  rating, notes/review per series, and **progress percentage** on the
  cards and the detail progress bar. The ribbon itself is configurable
  (color, transparency, width, shape, side) via the **Bookmark style**
  popover.
- **Per-series reading history** — every chapter update (+1 / Set /
  Clear) is logged with its date and signed delta; the detail page
  shows the timeline (newest first), a "chapters this week" count, and
  — once you have two-plus days of logged progress on a series with a
  known total — a **finish-date estimate** extrapolated from your
  reading pace.
- **Fully-completed emblem** — a small seal appears on a cover when the
  series is completed BOTH as a read and as a publication; its look is
  configurable (show/hide, seal / check / star style, color, size,
  transparency, corner) from the **Emblem style** popover on the library
  page.
- **Saved default sort** — a star (★) next to the Sort dropdown saves
  the current sorting as your default; it's stored server-side, so it
  applies in every browser and on every load.
- **Server-side settings** — layout, default sort, ribbon, emblem, theme
  and shelves are stored in the server's database (`GET`/`POST
  /api/settings`), so they follow the database: every browser and device
  you open the app in shows the same look, and the prefs survive
  browser-data wipes. Browsers upgrading from the old per-browser
  (localStorage/cookie) storage migrate automatically on first load.
- **Editable dropdowns** (`/options`) — the reading-status, type and
  publication-status vocabularies are yours: rename labels ("Completed"
  → "Complete"), add custom options ("Webtoon", "Re-reading"), reorder
  them, or remove unused ones — no code changes needed. Each option
  keeps a stable hidden **value** (the ID used by the database, URLs and
  CSV/JSON import), so renames never touch your data; built-ins the app
  relies on (the five reading statuses, publication "Complete") are
  locked against removal, and any option still used by a series refuses
  to be removed until you reassign those series.
- **Series metadata** — alternative/translated titles (searchable, feed
  duplicate detection), **publication status** (ongoing / complete /
  hiatus / canceled — editable via `/options`), and **released year**.
- **Covers** — upload a file (drag-and-drop onto the cover box with an
  instant preview, or click it to browse — both work without JS for
  picking), or **paste a URL** on the edit page.
- **Tag autocomplete** — the Tags field suggests your existing tags as
  you type (prefix match, case-insensitive; arrows + Enter/Tab
  complete).
- **Exact timestamps on hover** — every relative time ("2d ago") on
  cards and detail pages carries the absolute date-and-time tooltip.
- **Reading stats** (`/stats`) — currently reading, completed this month,
  average rating, reading streak (+ longest), status/type/publication-status
  breakdowns, top tags, and a 30-day activity strip.
- **Series grouping** — link spinoffs/prequels to a parent series;
  related series appear on both detail pages.
- **Duplicate detection** — exact (incl. **alternative-title matches**),
  typo-fuzzy (Levenshtein), and translated-alias (token overlap, e.g.
  "Akatsuki no Yona" vs "Yona of the Dawn") warnings before saving; batch
  imports can skip/update/create.
- **Bulk input** — batch **JSON API** (`POST /api/series/batch`: one
  request, one transaction for up to 1000 entries, dry-run supported) and a
  **CSV/JSON import page** (`/import`, paste or upload, dry-run supported).
- **Export/backup** — one-click **JSON** and **CSV** exports (both
  round-trip back through import), plus a **full backup** zip
  (`/backup`) bundling a consistent `VACUUM INTO` snapshot of the
  database, every uploaded cover, and restore instructions — stronger
  than JSON export: it also carries settings, shelves, streak counters
  and reading history.
- **Installable (PWA)** — a web manifest + theme color ship with the
  app icon, so "Add to Home Screen" / desktop install launches Fic
  Tally fullscreen and chromeless like a native app.
- **Mobile-friendly** — responsive layout from phone to desktop.
- Dark/light theme toggle.

## Documentation

This README is the entry point. Detailed documentation lives in
[`docs/`](./docs/):

- [**Architecture**](./docs/ARCHITECTURE.md) — folder layout, design
  decisions (why Go stdlib only, why `modernc.org/sqlite`, why partials
  not block inheritance, why filter in Go not SQL, how batch import stays
  efficient, how duplicate detection scores matches, etc.)
- [**HTTP Reference**](./docs/HTTP_REFERENCE.md) — every route with
  method, path, params, response codes, and redirect targets — including
  the batch API's JSON request/response shapes.
- [**Data Model**](./docs/DATA_MODEL.md) — Series + Entry fields, the
  SQLite schema (incl. `parent_id`, `completed_at`, `daily_reads`), enum
  values, and the rationale for the spec's field-split decisions.
- [**Development**](./docs/DEVELOPMENT.md) — build, run, test, and
  common-change patterns, plus gotchas (CSS escaping, driver name,
  `time.Time` zero value).
- [**Spec Compliance**](./docs/SPEC_COMPLIANCE.md) — maps every spec
  requirement to its implementation status; lists non-goals honored and
  user-directed additions (stats page, grouping, import/export).

## Build

```sh
CGO_ENABLED=0 go build -o fic-tally .
```

Produces one static binary, ~18 MB. No runtime dependencies, no CGO, no
external services. The SQLite driver is `modernc.org/sqlite` (pure Go) so
the binary is truly static; if you prefer `mattn/go-sqlite3` (CGO), swap
the import in `sqlite_store.go` and the driver name from `"sqlite"` to
`"sqlite3"`, then drop `CGO_ENABLED=0`.

## Run

```sh
./fic-tally                              # defaults: 127.0.0.1:4242, ./fic-tally.db
./fic-tally -addr 127.0.0.1:7531 -db /var/lib/fic-tally/db.sqlite
./fic-tally -addr 0.0.0.0:4242          # LAN-accessible (no auth; per spec)
```

Open `http://127.0.0.1:4242/` in a browser. The library is seeded with two
example series on first run (Iron Tide + Moonlit Cartographer) — delete
them from the UI whenever you're ready. If a `tsundoku.db` from an earlier
build exists, it is used as-is (schema columns are migrated automatically).

## Flags

| Flag          | Default      | Purpose                                            |
|---------------|--------------|----------------------------------------------------|
| `-addr`       | `127.0.0.1:4242` | `host:port` to listen on                       |
| `-db`         | `fic-tally.db` (falls back to `tsundoku.db` if only that exists) | SQLite database file path |
| `-templates`  | `templates`  | Templates directory (Glob `*.html`)                |
| `-static`     | `static`     | Static assets directory (CSS/JS/uploaded covers)  |

## Project layout

```
fic_tally/
├── go.mod              module + Go version + modernc.org/sqlite dep
├── app.go              app struct, template loading (+ option-label funcs), routes, main()
├── models.go           Series + Entry + Status + SeriesType + ChapterLog types
├── store.go            Store interface (Get / List / Save / SaveAll / ReadDays / Delete / Settings / SaveSettings / AppendLog / ChapterLog / Snapshot / *Usage / ClearPubStatusValue)
├── sqlite_store.go     SQLite implementation + migrations + seed
├── handlers.go         HTTP handlers + view-model helpers
├── settings.go         server-side UI prefs + shelves + /api/settings
├── options.go          editable dropdown vocabularies + /options handlers
├── dedup.go            duplicate-title scoring (normalized / Levenshtein / token overlap)
├── transfer.go         CSV/JSON import + CSV/JSON export + /import handlers
├── api.go              POST /api/series/batch (JSON batch API)
├── stats.go            /stats dashboard computation
├── backup.go           GET /backup — zipped full backup (VACUUM INTO snapshot + covers)
├── fic-tally           prebuilt static binary (linux/amd64)
├── fic-tally.db        SQLite DB (created on first run)
├── README.md           this file — entry point
├── scripts/smoke_test.sh  44-group end-to-end test suite
├── docs/
│   ├── ARCHITECTURE.md folder layout + design decisions explained
│   ├── HTTP_REFERENCE.md every route documented
│   ├── DATA_MODEL.md   Series + Entry fields, schema, enums, rationale
│   ├── DEVELOPMENT.md  build / test / extend patterns + gotchas
│   └── SPEC_COMPLIANCE.md spec → impl mapping, non-goals honored
├── templates/
│   ├── layout.html     shared header/footer partials
│   ├── library.html    library grid + filter toolbar + shelves + bulk bar
│   ├── detail.html     series detail (Continue reading first per spec + history)
│   ├── edit.html       add / edit series metadata form
│   ├── stats.html      reading statistics dashboard
│   ├── import.html     CSV/JSON batch import page
│   ├── options.html    dropdown-vocabulary editor
│   └── error.html      500 page
└── static/
    ├── css/app.css     ported from the mockup, extended (+ mobile)
    ├── js/app.js       settings sync, popovers, tag autocomplete, cover
                        drop/preview, bulk counter, card tag toggles
    ├── manifest.json   PWA web manifest (installable, standalone launch)
    ├── img/            app icon (favicon + topbar logo + apple-touch)
    └── covers/         uploaded cover images (created on first run)
```

## Routes

| Method | Path                          | Purpose                                |
|--------|-------------------------------|----------------------------------------|
| GET    | `/`                           | Library; query params `q` (multi-term + `#tag`), `status`, `type`, `tag` (comma list, AND), `sort` (default: saved `library.sort`, else `updated`) |
| GET    | `/series/new`                 | Add-series form                         |
| POST   | `/series/new`                 | Create series (shows a duplicate warning on fuzzy/exact title matches; `dup_confirm=1` overrides) |
| GET    | `/series/{id}`                | Series detail (Continue reading + tracking controls + related series) |
| POST   | `/series/{id}/progress`       | Update chapter (+1 / Set / Clear num)   |
| POST   | `/series/{id}/entry`          | Update entry (status, rating, notes, bookmark) |
| GET    | `/series/{id}/edit`           | Edit-series-metadata form               |
| POST   | `/series/{id}/edit`           | Update series metadata (tracking data untouched) |
| POST   | `/series/{id}/cover`          | Upload cover image (multipart, ≤ 8 MiB) |
| POST   | `/series/{id}/cover/url`      | Set cover from a remote http(s) URL     |
| POST   | `/series/{id}/cover/delete`   | Remove cover                            |
| POST   | `/series/{id}/delete`         | Delete series + entry (children detach) |
| GET    | `/stats`                      | Reading statistics dashboard            |
| GET    | `/import`                     | Batch import page (CSV/JSON)            |
| POST   | `/import`                     | Process import (paste or file upload)   |
| GET    | `/export/json`                | Full-library JSON export (download)     |
| GET    | `/export/csv`                 | Full-library CSV export (download)      |
| POST   | `/api/series/batch`           | JSON batch API — N entries, 1 transaction, `dry_run` + `duplicate_policy` |
| GET    | `/api/settings`               | Stored UI preferences as JSON            |
| POST   | `/api/settings`               | Upsert UI preferences (validated; per-server, not per-browser) |
| POST   | `/theme`                      | Toggle dark/light (stored as a server-side setting) |
| POST   | `/shelves/save`               | Save the posted filter+sort combo as a named shelf (server-side setting) |
| POST   | `/shelves/delete`             | Delete a shelf by name, redirect back |
| POST   | `/bulk/status`                | Apply a status to every selected series (`series_ids` repeated; checkboxes hidden behind the Select-multiple toggle) |
| GET    | `/options`                    | Dropdown-options editor (reading status / type / publication status) |
| POST   | `/options/save`               | Rename labels, add/remove/reorder options (validated) |
| GET    | `/backup`                     | Download a zip: DB snapshot (VACUUM INTO) + covers + RESTORE.txt |
| GET    | `/static/...`                 | Static assets (incl. `manifest.json`)  |

## Batch API quick start

```sh
curl -X POST http://127.0.0.1:4242/api/series/batch \
  -H 'Content-Type: application/json' \
  -d '{"series":[
        {"title":"Akatsuki no Yona","alt_titles":["Yona of the Dawn"],
         "type":"manga","status":"reading","pub_status":"ongoing",
         "year":2009,"current_chapter_number":120,"rating":9,"tags":["Fantasy"]}],
       "duplicate_policy":"skip"}'
```

Response: `{"created":n,"updated":n,"skipped":n,"failed":n,"results":[...]}`
with a per-row verdict. A bare `[...]` array is also accepted, and
`"dry_run":true` validates without writing. `GET /export/json` output can
be posted straight back (disaster-recovery round trip). `alt_titles`
accepts an array or a `"A; B"` string; `pub_status` is one of ongoing /
completed / hiatus / cancelled (the values — IDs — shown in mono on
`/options`; custom options you add there are valid too); `year` is 1-9999.

## Design

Tokens ported from `reading-tracker-mockup.html`, dark mode default with
a paper-cream light mode. Type stack: **Spectral** (display, series
titles), **Work Sans** (body/UI), **IBM Plex Mono** (chapter counts /
progress figures). Status colors as small dots, not full badges.

The signature visual element is the **ribbon** — a vertical bar with a
notched bottom edge that runs down each cover, its height proportional to
chapter progress. In the detail view it appears on the cover; the
progress bar beneath the chapter input is a secondary indicator for
clarity.

Detail-view priority is per spec: **chapter counter + Continue reading
CTA appear first**, with description/tags/notes BELOW — the most common
action on a series page is resuming the book, not editing metadata.

## Non-goals (per spec)

- No chapter release tracking
- No notifications
- No comments or social features
- No user accounts / multi-user libraries
- No recommendation engine
- No automatic manga scraping
- No crawler / multi-source aggregation
- No web-novel auto-fill in this build (the spec mentions paste-a-URL
  autofill but references an external scraper; not implemented here)
- Reading-history analytics: relaxed **by user request** — the stats page
  computes aggregates from data the tracker already keeps (one
  `daily_reads` counter row per day). See
  [Spec Compliance](./docs/SPEC_COMPLIANCE.md) for the departure note.

## Storage layer

`Store` is a small interface (`Get`, `List`, `Save`, `SaveAll`,
`ReadDays`, `Delete`) over joined `EntryWithSeries` rows. Only the SQLite
implementation exists today, but moving to a JSON-file or Postgres backend
is a second implementation of that interface, not a rewrite of handlers or
templates.

## Development notes

- Go 1.22+ ServeMux patterns (`GET /series/{id}`) are used; no chi/gorilla.
- Templates use the partials pattern (header/footer) rather than block
  inheritance, because `html/template`'s `{{block}}` across ParseGlob
  files clobbers per-page "content" definitions.
- Cover uploads: 8 MiB cap (`http.MaxBytesReader`), content-type sniffed
  from the first 512 bytes (never trusts the client Content-Type),
  filename is `<series_id>.<ext>` (series IDs are slug-safe, so
  path-injection-safe). Remote cover URLs are validated against an
  http(s)/local-prefix allowlist.
- The SQLite DB uses WAL journal mode and a single connection
  (`SetMaxOpenConns(1)`) to avoid "database is locked" errors.
- Filter/sort happens in Go memory, not SQL — the library is single-user
  and tiny; doing it in Go keeps the SQL simple and the filter logic
  auditable in one place (`handlers.go`).
- The reading history (`chapter_log`) is append-only and written only by
  the progress handler (when the numeric chapter actually changes), so
  imports and batch APIs never fabricate history. It is excluded from
  the CSV/JSON exports (those cover series+entry data) but IS included
  in the `/backup` zip.
- Schema migrations are additive (`parent_id`, `completed_at`,
  `daily_reads`, `alt_titles`, `year`, `pub_status`, `chapter_log`) and
  guarded by `PRAGMA table_info` checks, so older databases upgrade in
  place on first run. The v8 dropdown-options change renames LABELS only
  (Ongoing / Complete / Hiatus / Canceled) — stored values are untouched,
  so nothing migrates except `pub_status='upcoming'`, which is cleared
  to "" (unknown) once, at the same time the `options` settings group is
  seeded.
