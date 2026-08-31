# Architecture

This document explains the structure of the codebase and the decisions
behind it. The spec (`reading-tracker-spec-rev1.md`) calls for a
minimalist, self-hosted, single-user Go binary with no framework and
no runtime dependencies; the architecture below is what fell out of
taking that goal seriously.

## Folder layout

```
fic_tally/
├── go.mod              module + Go version + modernc.org/sqlite dep
├── go.sum              checksums
├── app.go              app struct, template loading (incl. option-label
│                       funcs closing over the app), route registration, main()
├── models.go           Series + Entry + Status + SeriesType + ChapterLog types
├── store.go            Store interface (Get / List / Save / SaveAll / ReadDays /
│                       Delete / Settings / SaveSettings / AppendLog / ChapterLog /
│                       Snapshot / StatusUsage / TypeUsage / PubStatusUsage /
│                       ClearPubStatusValue)
├── sqlite_store.go     SQLite implementation + migrations + seed
├── handlers.go         HTTP handlers + view-model helpers
├── settings.go         server-side UI prefs (layout / default sort / ribbon /
│                       emblem / theme / shelves): validation, /api/settings,
│                       theme, shelf save/delete, render injection
├── options.go          user-editable dropdown vocabularies + /options handlers
├── dedup.go            duplicate-title scoring (normalized / Levenshtein / token overlap)
├── transfer.go         CSV/JSON import + CSV/JSON export + /import + /export handlers
├── api.go              POST /api/series/batch (JSON batch API)
├── stats.go            /stats dashboard computation + streaks
├── backup.go           GET /backup — zipped full backup (VACUUM INTO snapshot
│                       + covers + RESTORE.txt)
├── fic-tally           prebuilt static binary (linux/amd64, 18 MB)
├── fic-tally.db        SQLite DB (created on first run, WAL mode)
├── README.md           top-level entry point
├── scripts/smoke_test.sh  44-group end-to-end HTTP test suite
├── docs/               this folder
│   ├── ARCHITECTURE.md
│   ├── HTTP_REFERENCE.md
│   ├── DATA_MODEL.md
│   ├── DEVELOPMENT.md
│   └── SPEC_COMPLIANCE.md
├── templates/
│   ├── layout.html     shared header/footer partials (+ PWA manifest link)
│   ├── library.html    library grid + filter toolbar + shelves row + bulk bar
│   ├── detail.html     series detail (Continue reading first per spec,
│   │                   + reading history & pace estimate)
│   ├── edit.html       add / edit series metadata form (+ cover drop zone,
│   │                   + tag autocomplete scaffolding)
│   ├── stats.html      reading statistics dashboard
│   ├── import.html     CSV/JSON batch import page
│   ├── options.html    dropdown-vocabulary editor
│   └── error.html      500 page
└── static/
    ├── css/app.css     ported from the mockup, extended (+ mobile breakpoints,
    │                   layout modes, configurable ribbon, configurable
    │                   completion emblem, tag chips, sort-default star, shelves,
    │                   bulk bar + card checkboxes behind the Select-multiple
    │                   toggle, tag-autocomplete popover, cover drop zone,
    │                   history list, pace line, options editor)
    ├── js/app.js       settings sync against POST /api/settings (debounced
    │                   writes + pagehide beacon, one-time migration of legacy
    │                   localStorage prefs), filter-form submit (button-driven
    │                   search), library layout switch, ribbon-style +
    │                   emblem-style popovers, card tag toggles, sort-default
    │                   star, theme-back-redirect, tag autocomplete, cover
    │                   drag-and-drop + instant preview, bulk selected counter
    ├── manifest.json   PWA web manifest (installable, standalone launch)
    ├── img/            app icon: icon.png (favicon + topbar logo),
    │                   apple-touch-icon.png (180px, opaque)
    └── covers/         uploaded cover images (created on first run)
```

The package is `main` — one binary, one package. Internal factoring is
by file, not by package: `models.go`, `store.go`, `sqlite_store.go`,
`handlers.go`, `dedup.go`, `transfer.go`, `api.go`, `stats.go`, `app.go`
are all `package main` in the same directory. For a project this size,
a single package keeps imports trivial and test fixtures close. If the
codebase grows past ~3,000 lines, the natural split is `internal/store`,
`internal/handlers`, `internal/models`, `internal/templates` — but
that's premature now.

## App rename (Tsundoku → Fic Tally)

The app was renamed at user request. What changed and what deliberately
didn't:

- **Changed**: module name (`fic-tally`), binary (`fic-tally`), UI
  strings, docs, default DB filename (`fic-tally.db`), export filenames.
- **Preserved**: an existing `tsundoku.db` is picked up automatically on
  first run when no `fic-tally.db` exists (nothing is renamed behind the
  user's back; delete or rename the old file to switch). Schema columns
  are migrated in place, so the rename is data-safe.

## Dependency choices

### Go stdlib for HTTP, not chi/gorilla/echo

The spec says `net/http` + `html/template` + `encoding/json` (or
`database/sql` for SQLite). Go 1.22's enhanced `ServeMux` introduced
method-qualified patterns (`GET /series/{id}`) and path wildcards, which
covers everything a small CRUD app needs without a router library.
Pulling in `chi` or `gorilla/mux` would add a dependency for ~6 routes.

### `modernc.org/sqlite`, not `mattn/go-sqlite3`

The spec mentions `mattn/go-sqlite3` in a parenthetical, but the spec's
primary goal — "one static binary, zero runtime" — is best served by
`modernc.org/sqlite`, a pure-Go reimplementation of SQLite that needs
no CGO. With `CGO_ENABLED=0`, the build produces a true static ELF
binary. `mattn/go-sqlite3` links against the system's libsqlite3 (or a
bundled C source via CGO), which produces a dynamically-linked binary
and ties the build to a C toolchain.

The driver name is the only API difference: `sql.Open("sqlite", ...)` vs
`sql.Open("sqlite3", ...)`. Swapping is a one-line change in
`sqlite_store.go` if you have a reason to prefer CGO (e.g., you want
SQLite's full-text-search extension, which `modernc.org/sqlite` does not
yet expose).

### No ORM

`database/sql` + hand-written SQL. The schema is two tables, the queries
are six. An ORM would add a dependency, hide the SQL, and provide no
abstraction value at this size.

## Template strategy: partials, not block inheritance

Go's `html/template` package supports `{{block "name" .}}...{{end}}`
for template inheritance — define a base layout, then override the
"content" block in each page template. The catch is that `ParseGlob`
parses all templates into one `*template.Template` set, and the set
only keeps one definition per template name. If `library.html` and
`detail.html` both `{{define "content"}}`, the last-parsed one wins,
clobbering the other.

The standard workaround is to `Clone()` the set per request and re-parse
the page template into the clone. That's expensive and error-prone.

The pattern used here is the simpler alternative: shared partials. The
`layout.html` file defines two partial templates, `{{define "header"}}`
and `{{define "footer"}}`, containing the topbar / `<html>` shell /
`<script>` includes. Each page template (`library.html`, `detail.html`,
`edit.html`, `error.html`) is self-contained and pulls in the partials
via `{{template "header" .}}` and `{{template "footer" .}}`. No
clobbering because no two page templates define the same name.

## Filter and sort happen in Go, not SQL

The library page supports filtering by status, type, tags, and search;
sorting by last-read, title, rating, or last-updated (the default —
the saved `library.sort` setting can change it). Tag filtering accepts
a comma-separated list with AND semantics (`?tag=isekai,romance`), and
the search box parses `#tag` tokens plus multi-term free text. All
filters and sorts run in Go over an in-memory `[]EntryWithSeries`
loaded by `Store.List()`.

This is a deliberate trade-off. The alternative is to push the filter
and sort into the SQL `SELECT`, which would be more efficient at scale
but adds query-construction complexity (especially for "tag contains X
in JSON array" — that's a `JSON_EACH()` join in SQLite). For a
single-user library that will likely never exceed a few hundred entries,
the in-memory approach is simpler, more auditable (the entire filter
logic lives in one ~40-line block of `handlers.go`), and easy to
replace with SQL later if the library grows.

The hook for that replacement is the `Store` interface: `List()` returns
`[]EntryWithSeries` today. A future `ListFiltered(filter Filter)` method
on the interface could push the filter into SQL without touching
handlers.

## SQLite connection policy

`SetMaxOpenConns(1)` on the `*sql.DB`. SQLite serializes writes anyway
(holding an exclusive lock during a transaction), so a single connection
avoids "database is locked" errors when concurrent HTTP requests try to
write. Reads can proceed in WAL mode while a write is in flight, but
the single-connection policy means a read in flight will block briefly
on a queued write — acceptable for a single-user app where contention
is essentially nil.

WAL mode is set via `PRAGMA journal_mode=WAL` at startup. WAL gives
non-blocking reads plus crash resilience. The `-wal` and `-shm` files
appear next to `fic-tally.db` at runtime; they're part of the database
and should be backed up together with the main file.

## PRG pattern for mutations

Every state-changing POST — add, edit, progress, entry update, cover
upload, cover delete, series delete, theme toggle — uses the
Post/Redirect/Get pattern:

1. Handler validates form input.
2. Handler calls `Store.Save` / `Store.Delete`.
3. Handler responds with `303 See Other` to a GET URL (usually the
   detail page or library).
4. Browser's GET request renders the updated state.

This avoids the "resubmit on refresh" footgun, makes the URL bar
bookmarkable, and means the server is stateless across the mutation.

## Cover upload safety

Cover uploads go through three layers of defense:

1. **Size cap.** `r.ParseMultipartForm(2 << 20)` rejects bodies over
   2 MiB before they hit disk. Covers are small images; 2 MiB is
   generous.
2. **Content-type sniffing.** The first 512 bytes are read and passed
   to `http.DetectContentType`. The user-supplied `Content-Type` header
   and the file extension are ignored. Only `image/png`, `image/jpeg`,
   `image/gif`, and `image/webp` are accepted.
3. **Filename derivation.** The destination filename is
   `<series_id>.<ext>` — never the user-supplied filename. Series IDs
   are produced by `slugify()` in `app.go`, which keeps only lowercase
   alphanumerics and dashes. This is path-injection-safe: even a
   malicious upload can't write outside `static/covers/`.

## Template function map

`loadTemplates()` in `app.go` registers a `template.FuncMap` with
helpers that keep template logic small and avoid exposing Go internals
to the template layer:

| Function           | Purpose                                                  |
|--------------------|----------------------------------------------------------|
| `firstChar`        | First rune of a string, uppercased. Used for the cover-letter placeholder when no image is set. Handles Unicode correctly, unlike `printf "%c"` on a string. |
| `statusDotClass`   | Returns `dot-reading` / `dot-plan` / `dot-hold` / `dot-dropped` / `dot-done` for a status string. Used as a CSS class suffix because `html/template`'s CSS-context escaping rejects inline `var(--status-...)` interpolation. |
| `intStr`           | Returns the integer value of an `*int`, or `""` if nil. For form `<input value=...>` on nullable fields like `rating`. |
| `floatStr`         | Returns a float formatted without trailing `.0`, or `""` if nil. For `total_chapters` and `current_chapter_num` inputs. |
| `joinStr`          | Joins a `[]string` with a separator. Used for the tags CSV input. |
| `lower`, `hasPrefix`, `trimSuffix` | Thin wrappers over `strings` package funcs. |

> **Why no `selectedAttr`/`checkedAttr` helpers?** An earlier version had
> FuncMap helpers returning `"selected"`/`"checked"`/`""` for the
> attribute-name position in `<option>`/`<input>` tags. They were removed
> because `html/template` substitutes the sentinel `ZgotmplZ` for an
> empty-string action placed in the "attribute name" position (i.e., right
> after another attribute). Templates now inline `{{if eq ...}} selected{{end}}`
> and `{{if .X}} checked{{end}}` instead — these emit nothing when the
> condition is false and a literal `selected`/`checked` (with a leading
> space) when true, both of which `html/template` accepts in attribute
> position without sentinel substitution.

## Environment assumptions

- The process can write to the working directory (for `fic-tally.db`
  and `static/covers/`).
- The `-templates` and `-static` flags resolve relative to the
  process's current directory unless absolute paths are passed.
- The Go toolchain is available at build time; at runtime, the binary
  has no toolchain dependency.
- Outbound DNS is needed only for Google Fonts (`<link>` to
  `fonts.googleapis.com` in the layout); the app works offline once
  fonts are cached by the browser.

## Batch input: one request, one transaction

Bulk data enters through three doors — the batch API
(`POST /api/series/batch`), the import page (`POST /import`), and the
export round-trip — all funneling into the same two functions:

- `resolveImport` (transfer.go) — validates every row, resolves IDs
  (slugify + `-2`/`-3` suffixes for same-file collisions), applies the
  duplicate policy, and produces per-row verdicts. Pure function over
  the existing library, so a `dry_run` is just "run it, then drop the
  batch".
- `Store.SaveAll` (sqlite_store.go) — writes the resolved batch inside
  ONE transaction. A single SQLite transaction amortizes the fsync
  across all rows: importing 100 entries costs one BEGIN/COMMIT instead
  of 100, which is the difference between milliseconds and seconds on
  WAL-mode SQLite.

Caps keep abuse honest without hurting real use: 1000 rows/request,
4 MiB JSON bodies (API) / 8 MiB uploads (import). Per-row validation
failures don't abort the batch — they're reported inline with
`action:"error"` and the rest commits; only a mid-transaction storage
failure rolls everything back.

## Duplicate detection design (dedup.go)

Matching considers the **full name set on both sides** — the main title
plus all alternative titles — so an incoming title that equals a stored
alternative title (or vice versa) is a strong duplicate, exactly the
"Akatsuki no Yona" vs "Yona of the Dawn" case once the alias is
recorded. Three signals, cheapest first, no metadata service required:

1. **Normalized exact** on any title/alt-title pair — lowercase, strip
   non-alphanumerics, collapse whitespace. Catches "Iron Tide!" vs
   "iron tide" and every alt-title equality. This is the only *strong*
   signal: import/API policies (skip/update) act on it.
2. **Levenshtein similarity ≥ 0.80** (best score across all name
   pairs) — catches typos and small edits ("Iorn Tide"). Rolling-row
   O(n·m) is plenty for titles.
3. **Significant-token overlap ≥ 0.5** (of the smaller token set, best
   across all pairs) — catches translated aliases like "Akatsuki no
   Yona" vs "Yona of the Dawn", where edit distance is useless but the
   distinctive word matches. Stopwords (the/a/of/no/na/…) and
   sub-3-rune tokens are dropped first so "the" never matches.

Signals 2 and 3 are *advisory*: the add form shows a warning with a
"Save anyway" button (`dup_confirm=1`), and imports merely annotate the
row ("note: similar to …"). A false positive therefore never silently
drops or blocks data — the strong-only policy for automated paths was
chosen deliberately.

## Stats page (stats.go)

All aggregates are computed in Go from `List()` + `ReadDays()`; there
are no aggregate SQL queries to keep in sync with the schema, and at
single-user library sizes the O(n) passes are instant. Two design
points worth knowing:

- **Streaks need event data.** `last_read_at` only records each series'
  most recent read, so reading the same series two days in a row
  overwrites yesterday's evidence. The `daily_reads` table (one counter
  row per UTC day, bumped inside `saveTx` when progress advances) gives
  exact streaks and the 30-day activity strip while storing nothing
  per-series — deliberately short of the "reading history analytics"
  non-goal (see SPEC_COMPLIANCE).
- **"Completed this month" needs a transition timestamp.**
  `completed_at` is set when status transitions INTO completed and
  cleared on the way out, managed centrally in `saveTx` so every write
  path (form, progress handler, batch API, CSV import) behaves
  identically. `updated_at` would have been a misleading proxy (it
  bumps on any edit).

## Series grouping

`series.parent_id` is a soft reference (slug string, no DB-level FK):
deleting a parent clears the children's `parent_id` in the same
transaction (`UPDATE … SET parent_id='' WHERE parent_id=?`) instead of
cascading deletes or FK violations. One level of linkage is exposed in
the UI (parent chip + spinoff chips on both detail pages); deeper
universes are representable in the data but render flat, which matches
how often that actually matters in a personal library.

## Server-side UI preferences (settings.go)

Layout mode, saved default sort, ribbon style, emblem style and theme
are stored in the server's SQLite `settings` table — one row per group,
`value` being the canonical JSON for that group — so preferences follow
the database instead of the browser. This was a deliberate change from
the earlier per-browser localStorage/cookie storage: a single-user app
running on one machine is opened from several browsers/devices, and the
look should not depend on which one.

The `library` group holds the saved default sort (a star button next to
the Sort dropdown writes it; `handleLibrary` applies it whenever `/`
loads without an explicit `?sort=`). The built-in default sort is
`updated` (last touched first) — it surfaces metadata edits too, which
`last_read` misses.

The write path is strictly validated server-side
(`parseSettingsPatch`): enums, bounded numbers and `#rrggbb` colors
only, unknown groups rejected. This isn't just input hygiene — the
stored blobs are re-rendered into every page (as the `#ft-settings`
JSON blob and `data-*` attributes on `<html>`), so validation doubles
as output safety. The blob is injected as `template.JS` because
html/template would otherwise escape a string in a script element as a
JS string literal (double-encoding it).

The read path is centralized in `app.render`: every page gets
`SettingsJSON`, `Theme` and the attribute values injected, so new pages
gain preference support for free and handlers no longer pass `Theme`
themselves. `data-*` attributes are rendered directly onto `<html>`
(preferences apply even with JavaScript disabled); CSS custom
properties cannot be templated into a `style` attribute (html/template
CSS escaping drops custom properties), so the inline pre-paint script
in `layout.html` sets those from the blob.

Client sync (`static/js/app.js`): changes apply immediately in the DOM
and POST to `/api/settings` behind a 250 ms debounce — one request per
slider gesture, not one per pixel — with a `pagehide` `sendBeacon`
flush so a mid-debounce tab close doesn't lose the last change.
Beacons can't send `application/json`, so the server parses the body as
JSON regardless of Content-Type. Cross-origin POSTs (mismatched
`Origin` header) are rejected with `403` to block drive-by preference
vandalism from other web pages.

Upgrade path: on first load after upgrading from a localStorage build,
`mergeLegacyPrefs` adopts any legacy localStorage groups the server
doesn't have, pushes them through the same debounced POST (so a user
click milliseconds later overwrites the queued legacy value — no write
race), and deletes the localStorage keys once the server confirms. The
legacy `theme` cookie is adopted server-side in `resolveTheme`. The
pre-paint script also falls back to legacy localStorage values for
groups the server lacks, so upgraders never see a flash of defaults.

One bug worth remembering: `mergeLegacyPrefs` executes immediately and
calls `persistPrefs`, which writes `pendingPatch[k] = …`. The
`pendingPatch`/`flushTimer` `var`s must be declared AND initialized
BEFORE the IIFE — a hoisted-but-uninitialized `var` is `undefined`, and
the resulting TypeError aborts the entire script for upgrading users
(browser testing caught it; the smoke suite can't see JS runtime
errors).

## Saved views — shelves (settings.go)

A shelf is a name plus a canonical query string capturing a library
view (`q` / `status` / `type` / `tag` / `sort`, empties dropped, keys
sorted by `url.Values.Encode`). Reusing the settings table (as the
`shelves` group) rather than adding a dedicated table keeps one
persistence mechanism for everything that "follows the database", and
the group validation (`parseShelvesPrefs`) re-canonicalizes each stored
`params` string on load — a hand-edited row carrying unknown keys or
invalid enums is rejected wholesale instead of ever reaching an
`href`.

Two deliberate design points:

- **The save form posts the EFFECTIVE sort.** The hidden `sort` input
  carries the sort the page is actually rendering with (saved default
  or explicit `?sort=`), so a shelf always pins a complete view.
  Active-chip matching then compares the canonicalized current view —
  effective sort included — against each shelf's params: a shelf
  lights up only when the view it saved is *exactly* what you're
  looking at.
- **The whole row works without JavaScript.** Chips are plain links;
  the "Save view…" popover is a native `<details>` element; save and
  delete are ordinary form POSTs with PRG redirects. That's cheaper
  and more robust than wiring it through the JS settings sync, which
  exists for slider-style instant feedback shelves don't need.

## Per-series reading history (chapter_log)

`chapter_log` is an append-only table: (series_id, chapter, label,
signed delta, timestamp). Only `handleProgress` writes it, and only
when the numeric chapter actually changes — no-op saves, metadata
edits, imports and the batch API never fabricate history. The delta is
signed (a Set backwards logs negative), and "chapters this week" and
the pace estimate only ever count positive deltas. Rows are deleted
with their series (meaningless once the series is gone) but survive
everything else, including `/backup`.

The finish-date estimate (`paceEstimate`) is deliberately
conservative: it needs a known total, current below total, and positive
progress on 2+ distinct days within a 14-day window — otherwise the UI
shows nothing rather than a made-up date. The rate is
chapters ÷ observed-days × 7 (observed days measured from the earliest
in-window entry, minimum 1); remaining ÷ rate gives weeks left. On a
fresh tracker with a single day of history the estimate stays hidden —
one binge day is not a pace.

## Bulk status changes

The entire library grid lives inside one `<form method="post"
action="/bulk/status">`. Each card is wrapped in a `.card-wrap` div
holding the card link *plus* a checkbox `<label>` — the checkbox is a
sibling of the `<a>`, never a child, because clicking a form control
inside an anchor navigates in every major browser. The bulk bar
submits every checked `series_ids` together; each series then goes
through the normal `Store.Save` path, so `completed_at` transitions
behave exactly like single-series edits. JavaScript contributes only
the live "N selected" counter — the flow itself is plain HTML.

v8 hid the whole apparatus behind a **Select multiple** toggle in the
viewbar: the checkboxes sat on every cover full-time, which was noisy
for browsing. The toggle is a `<label for="bulk-mode">` pointing at a
**nameless** checkbox inside the bulk form (nameless → never
submitted); CSS `:has()` — `.bulk-form:has(#bulk-mode:checked)` —
reveals the bar and the card checkboxes while it is checked, and
`section.view:has(…)` styles the toggle button itself. Zero JavaScript,
and the mode naturally resets on the next page load (the PRG back
after applying re-hides it). The checkbox is visually hidden but
focusable (1×1 px, opacity 0), so keyboard users can still reach the
toggle. The cost: browsers without `:has()` support (pre-2023) never
see the bulk tools — an accepted trade-off for a self-hosted tracker,
documented here.

## Full backup (backup.go)

`GET /backup` zips three things: a `VACUUM INTO` snapshot of the
database, every file in `static/covers/`, and a RESTORE.txt. VACUUM
INTO (SQLite ≥ 3.27) is the key choice: copying the `.db` file of a
live WAL database can miss the `-wal` file's contents or catch a
half-written page, while VACUUM INTO produces a clean, compacted,
self-contained copy under proper locking with concurrent requests
uninterrupted. The snapshot lands in a temp file first (VACUUM INTO
refuses to overwrite an existing path) and the zip streams straight to
the response, so a large cover library never buffers in memory.

Why not extend the JSON export instead? The exports intentionally
cover *portable* series+entry data; the backup covers *everything*
(settings, shelves, streak counters, reading history, covers) for
disaster recovery. Different jobs, different formats.

## PWA manifest

A 20-line `static/manifest.json` (name, icons, `display: standalone`,
theme color) plus `<meta name="theme-color">` make the app
installable: "Add to Home Screen" on Android/iOS and desktop install
in Chromium give Fic Tally its own chromeless window with the app
icon. It is served as `.json` rather than `.webmanifest` because Go's
`http.FileServer` derives Content-Type from the extension —
`application/json` arrives correctly while a bare `.webmanifest`
would come back as `application/octet-stream` without registering a
MIME type. Chrome accepts either media type. There is deliberately no
service worker: the app already requires the server to be running
(it's a tracker backed by SQLite, not an offline web page), and a SW
would only add cache-staleness risk — the exact class of bug the
`noCache` middleware + `?v=` cache-busting exist to prevent.


## Dropdown options — editable vocabularies (options.go)

The reading-status, type and publication-status dropdowns were
hardcoded Go slices until v8, when the user asked to change the
publication-status labels AND to own those lists going forward. They
are now data: a `{value, label}` list per vocabulary in the `options`
settings group, editable on `/options` (rename / add / remove /
reorder), cached in `app.opts` behind an RWMutex and swapped wholesale
on save.

**The value/label split is the load-bearing decision.** The value is
the permanent ID — what the database stores, what filter URLs,
shelves, CSV/JSON import and the batch API speak. The label is
display-only. Renaming "Completed" → "Complete" therefore touches zero
rows, zero shelves, zero exports; only the rendered text changes.
That is also why the v8 vocabulary change needed no data migration
except the retired `upcoming` value (rows cleared to "" once, during
options seeding — the absent-group path in `initOptions`, ordered
before the group is persisted so a crash between the two writes just
re-runs the idempotent UPDATE).

Why not "the string is both the value and the label"? Because then
every rename is a data migration, and a textarea-style editor can't
distinguish "renamed row 3" from "deleted row 3, added a new one" —
the classic edit-distance problem. Keying every form field by the
immutable value (`label_status_dropped`, `del_type_webtoon`) makes
each operation explicit and unambiguous, and lets a stale form (one
missing a just-added option) merge cleanly: absent keys keep stored
values, present-but-empty labels are rejected.

**Two guards keep dynamic lists from breaking app logic:**

1. *Protected values.* The five reading statuses are load-bearing
   (`completed` → completed_at transitions + the completion emblem;
   `reading` → the Currently-Reading tile; `plan to read` → the
   new-series/import default; all five → the fixed stats tiles), and
   pub_status `completed` drives the emblem. They can be renamed but
   never removed — enforced in the save handler AND re-checked when a
   stored blob is loaded (`parseOptionLists`), so a hand-edited DB row
   can't smuggle the anchors away either. `defaultType` handles the
   one soft spot: if the user removes the `manga` default, type-less
   form/import rows fall back to the first current option instead of
   failing.
2. *In-use removal guard.* Three GROUP BY queries (`StatusUsage` /
   `TypeUsage` / `PubStatusUsage`) count live usage; an option still
   referenced by a series refuses removal with the count and a hint to
   reassign first (the bulk-status tool is the natural way). No row can
   end up holding an orphaned value, so every select always matches its
   stored data.

**One list, everywhere.** Validation reads `a.options()` at every
gate: form parsing, entry edit, bulk status, import (CSV/JSON), the
batch API, shelf canonicalization and the stats breakdowns. A value is
valid everywhere or nowhere. Labels render through template funcs
(`statusLabel` / `typeLabel` / `pubStatusLabel`) that close over the
app — the funcs are bound at template-parse time but resolve at render
time, so a rename applies on the very next page with no reload of the
template set. Custom statuses get the `dot-plan` dot color fallback;
unknown stored values (impossible via the guards, survivable via
hand-edited DBs) render as their raw value rather than blank.

The `options` group is deliberately NOT writable through
`POST /api/settings` — the settings API's per-group validators
(uiSettings) don't know these rules, so the group is left out of its
switch and rejected as unknown. One write path, one validation story.
