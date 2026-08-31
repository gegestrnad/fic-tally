# Spec Compliance

Maps the spec (`reading-tracker-spec-rev1.md`, the authoritative
revision) to the implementation. Reads top-to-bottom as "what the spec
asked for, and where (or whether) this build delivers it."

## Data model

| Spec field                          | Status | Implementation notes                                   |
|-------------------------------------|--------|--------------------------------------------------------|
| `Series.id`                          | ✓      | Slug of title; stable across edits.                    |
| `Series.title`                       | ✓      |                                                        |
| `Series.type`                        | ✓      | Five enumerated values, full canonical strings.        |
| `Series.author`                      | ✓      |                                                        |
| `Series.description`                 | ✓      |                                                        |
| `Series.cover_url`                   | ✓      | Accepts uploaded (`/static/covers/...`) or external URL. |
| `Series.tags[]`                       | ✓      | JSON-encoded `TEXT` column.                            |
| `Series.source_url`                  | ✓      | Stored; not scraped.                                   |
| `Series.total_chapters` (nullable)   | ✓      | `REAL` column; nil when unknown.                       |
| `Series.total_is_known` (bool)        | ✓      | `INTEGER` 0/1. Drives `210+` UI treatment.             |
| `Series.created_at`                  | ✓      |                                                        |
| `Entry.id`                            | ✓      | `series_id` doubles as the entry's primary key.       |
| `Entry.series_id`                     | ✓      | FK with `ON DELETE CASCADE`.                           |
| `Entry.status`                        | ✓      | Five enumerated values, full canonical strings.       |
| `Entry.current_chapter_number` (nullable float) | ✓      | `REAL` column; nil when no numeric position. |
| `Entry.current_chapter_label` (string, always populated) | ✓      | `TEXT NOT NULL DEFAULT ''`. |
| `Entry.rating` (nullable 1-10 int)     | ✓      | `INTEGER` column; nil when unset.                      |
| `Entry.notes`                         | ✓      |                                                        |
| `Entry.bookmark_url`                  | ✓      | Drives the Continue-reading button's href.            |
| `Entry.bookmark_label`                | ✓      | e.g. `"Chapter 143"`; renders as `Continue reading → Chapter 143`. |
| `Entry.updated_at`                    | ✓      | Bumps on every save.                                   |
| `Entry.last_read_at`                  | ✓      | Bumps only on chapter advancement (Save w/ `advanceProgress=true`). |

The rationale paragraphs in the spec — about `current_chapter_number`
vs `current_chapter_label`, `total_chapters` + `total_is_known`,
`updated_at` vs `last_read_at`, and the 1-10 rating — are honored
verbatim. See `docs/DATA_MODEL.md` for the field-by-field explanation.

## Tech stack

| Spec recommendation                  | Status | Implementation notes                                                                       |
|--------------------------------------|--------|----------------------------------------------------------------------------------------------|
| Primary: Go + `net/http` + `html/template` + `encoding/json` | ✓      | Go 1.22 stdlib ServeMux patterns. No chi/gorilla. |
| JSON file storage                     | ✗      | Skipped per user instruction.                                                                |
| SQLite (the spec's "later" option)   | ✓      | Used now. Via `modernc.org/sqlite` (pure Go, no CGO) instead of `mattn/go-sqlite3`. See below. |
| `Store` interface (Get, List, Save, Delete) | ✓      | Defined in `store.go`, implemented by `SQLiteStore` in `sqlite_store.go`.                |

### Why `modernc.org/sqlite` instead of `mattn/go-sqlite3`

The spec calls for "one static binary, zero runtime." `mattn/go-sqlite3`
requires CGO and links against the system libsqlite3 (or a bundled C
source), producing a dynamically-linked binary. `modernc.org/sqlite`
is a pure-Go reimplementation of SQLite that needs no CGO — with
`CGO_ENABLED=0`, the build is truly static. The swap is one line in
`sqlite_store.go` if you ever want CGO back (e.g., to use SQLite's
full-text-search extension).

## Feature set

### Library management

| Spec feature                                  | Status | Implementation notes                                                       |
|-----------------------------------------------|--------|----------------------------------------------------------------------------|
| Add manually                                  | ✓      | `GET /series/new` (form) + `POST /series/new` (create).                    |
| Paste-a-URL auto-fill for web novels         | ✗      | Skipped per user instruction. `source_url` field exists but is not scraped. |
| Edit                                          | ✓      | `GET /series/{id}/edit` + `POST /series/{id}/edit`.                       |
| Delete                                        | ✓      | `POST /series/{id}/delete`. Idempotent.                                   |
| Tagging                                        | ✓      | Comma-separated input; stored as JSON array; queryable via `?tag=`.      |

### Progress tracking

| Spec feature                                  | Status | Implementation notes                                                       |
|-----------------------------------------------|--------|----------------------------------------------------------------------------|
| Status (reading / plan to read / on hold / dropped / completed) | ✓      | Full canonical strings in DB and UI.                                       |
| Chapter tracked as number + label            | ✓      | Two columns. UI: `btn_plus` advances the number; label auto-set if empty. "Clear num" button sets number to nil. |
| Quick +1 on the numeric side                 | ✓      | `btn_plus` button on the detail page.                                      |
| Bookmark as a labeled resume point            | ✓      | `bookmark_label` + `bookmark_url`; UI shows `Continue reading → Chapter 143`. |
| Rating (1-10)                                 | ✓      | Not 5-star. Number input with min=1, max=10, nullable.                    |
| Notes                                          | ✓      | Textarea in the "Your tracking" form on the detail page.                  |
| `last_read_at` drives "recently active" sort | ✓      | Default `sort=last_read`. `updated_at` is separate and only drives the `sort=updated` option. |

### Discovery

| Spec feature                                  | Status | Implementation notes                                                       |
|-----------------------------------------------|--------|----------------------------------------------------------------------------|
| Grid / list toggle                            | Partial | Grid only. List view not implemented; the spec lists it as a feature but the mockup doesn't show a list view, and grid is the only mode here. |
| Filter by status                              | ✓      | `?status=<value>`.                                                          |
| Filter by type                                | ✓      | `?type=<value>`.                                                            |
| Filter by tag                                 | ✓      | `?tag=<value>` (exact, case-insensitive match).                           |
| Sort by last read                             | ✓      | `?sort=last_read` (default).                                                |
| Sort by title                                 | ✓      | `?sort=title` (case-insensitive asc).                                       |
| Sort by rating                                | ✓      | `?sort=rating` (desc, nulls last).                                         |
| Sort by last updated                          | ✓      | `?sort=updated`.                                                            |
| Search                                        | ✓      | `?q=<substring>`. Matches title, author, tags, description.                |

### Detail view priority

> Visual weight goes to the chapter counter and a prominent Continue
> reading action first, with description/tags/notes below it, not the
> other way around.

✓ Honored. The detail template (`templates/detail.html`) renders, in
order:

1. Title + byline (small)
2. **Continue-reading CTA** (boxed, with left-border ribbon accent) +
   chapter counter + `+1` / `Set` / `Clear num` controls + progress
   bar + "last read X ago" line
3. About (description)
4. Tags
5. Your tracking (status / rating / notes / bookmark)

The mockup had it backwards — chapter controls after the description
block. This build fixes that.

## Non-goals (all honored)

The spec lists non-goals "written down explicitly so scope doesn't
drift." Each is checked against the implementation:

- **No chapter release tracking** — ✓ No release feed, no "next
  chapter on date X" feature. The `total_chapters` field is the only
  chapter-count metadata, and it's the user's manually-entered
  estimate.
- **No notifications** — ✓ No background goroutines, no scheduler, no
  notification service.
- **No comments or social features** — ✓ No comments table, no
  user-to-user features (and no users to begin with).
- **No user accounts / multi-user libraries** — ✓ No `users` table,
  no auth middleware, no sessions. The single user is implicit.
- **No recommendation engine** — ✓ No recommendation logic anywhere
  in the codebase.
- **No automatic manga scraping** — ✓ The `source_url` field is
  stored but never fetched. The spec explicitly notes manga has no
  consistent scraping target.
- **No reading history analytics** — ⚠ **Relaxed by user request** after
  the original build: a `/stats` dashboard now shows currently-reading
  count, completed-this-month, average rating, streaks, status/type
  breakdowns, top tags, and a 30-day activity strip. The relaxation is
  deliberately minimal — the only new data is one counter row per day
  (`daily_reads`, no per-series detail) plus `completed_at` transition
  timestamps; everything else aggregates data the tracker already kept.
  No charts library, no per-series history views, nothing that edges
  toward the "reinvented NovelUpdates" path the non-goals guard against.
- **No crawler / multi-source aggregation** — ✓ Single-user, single-
  source. The closest thing is `source_url` and `bookmark_url`, both
  of which point at single URLs the user pasted.

## Design system

### Palette

| Token        | Hex (dark) | Hex (light) | Status |
|--------------|------------|-------------|--------|
| `ink-950`    | `#15181d`  | `#ece4d3`   | ✓      |
| `ink-850`    | `#1d2127`  | `#e1d7c2`   | ✓      |
| `ink-700`    | `#2b313a`  | `#cbbfa5`   | ✓      |
| `paper-50`   | `#eae6da`  | `#2a2620`   | ✓      |
| `paper-400`  | `#8b93a0`  | `#6b6558`   | ✓      |
| `ribbon-500` | `#a8342a`  | `#a8342a`   | ✓      |
| `ribbon-400` | `#c04a3d`  | `#8f2b22`   | ✓ (added as hover variant) |
| Status colors (sage / slate / amber / muted red / plum) | — | — | ✓ As `--status-*` tokens in `:root` |

### Type

| Role  | Family           | Status |
|-------|-------------------|--------|
| Display (titles, headings) | Spectral | ✓ Loaded via Google Fonts CDN |
| Body / UI | Work Sans | ✓ |
| Data (chapter counts, progress) | IBM Plex Mono | ✓ |

### Layout

- Library is a shelf: filter/search bar over a card grid ✓
- Each card a cover with the ribbon overlay ✓
- Detail view is an open book spread: fixed-width left "page" for
  the cover, wider right "page" for content ✓
- Hairline spine between the two "pages" via `border-right` on
  `.spread-left` ✓

### Ribbon as the signature element

> The ribbon's position/length is the signature element, it doubles as
> the progress indicator on every cover card and in the detail view,
> rather than a generic percentage bar.

✓ Implemented as a vertical bar with a notched bottom edge
(`clip-path: polygon(...)`), running down each cover. Height is the
progress percentage. In the detail view, the ribbon appears on the
cover (left "page" of the spread). The horizontal progress bar
beneath the chapter input is a secondary indicator for clarity — the
spec's intent (ribbon as primary indicator) is preserved on the cover.

**User-requested extension:** the ribbon is now customizable via the
"Bookmark style" popover on the library page — color (presets + free
picker), transparency, width, shape (tag / line / triangle / round),
and side (left / right). Defaults reproduce the original crimson
notched tag, so the spec look remains the out-of-the-box experience.

## Mockup defects fixed

The spec calls the mockup "visually representative but structurally
stale." The build fixes the listed defects:

| Mockup defect                                       | Fix in this build                                            |
|-----------------------------------------------------|---------------------------------------------------------------|
| 5-star rating instead of 1-10                       | Number input with `min=1 max=10`, stored as nullable `INTEGER`. |
| Chapter controls before "Continue reading" action  | Detail view reorders: Continue-reading CTA first, then chapter counter, then description / tags / notes below. |
| Hardcoded relative timestamps (`"2d ago"`)          | `relTime()` computes real relative time from `last_read_at`; `"2d ago"` appears only because Iron Tide was seeded 2 days ago. |
| (Other mockup staleness not enumerated in spec)     | Status dot uses CSS class (`dot-reading`) not inline `var()`; total chapters shows `210+` when `total_is_known=false`; ribbon height caps at 100% for over-complete entries. |

## Open risks

The spec lists three open risks. Status:

- **Scope creep** — ✓ Mitigated by the explicit non-goals list. No
  scope-crept features were added.
- **Manga metadata (no consistent scraping target)** — ✓ Honored.
  Manga entries are manual; the spec's note that "there's no clean
  scraping target across manga aggregators" is the reason auto-fill
  is not implemented for any series type in this build (not just
  manga).
- **Sync (flat JSON file has no built-in multi-device sync)** — The
  spec lists this as "not something the base design gives you for
  free." This build uses SQLite instead of a flat JSON file, but the
  sync situation is unchanged: SQLite is a single file with no
  built-in multi-device sync. If multi-device support is needed,
  the spec suggests Syncthing, a sync endpoint, or SQLite plus a
  backup cron — none of which are implemented here. The `-db` flag
  lets you point the binary at any path (including a
  Syncthing-managed directory) without code changes.

## Departures from spec (with reasons)

| Departure                                       | Reason                                                                                                |
|--------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| `modernc.org/sqlite` instead of `mattn/go-sqlite3` | Pure Go, no CGO, true static binary. The spec's mention of mattn is incidental. Drop-in swap is one line. |
| SQLite now instead of starting with JSON file     | User instruction at build time. The `Store` interface is unchanged; a JSON impl could be added later. |
| Web-novel auto-fill not surfaced                  | User instruction at build time. The `source_url` field exists; a scraper module could be added. |
| List view not implemented                         | The spec lists "Grid / list toggle" as a feature. Only grid is implemented here; the toggle is a UI-only future addition. The mockup only shows grid. |
| Cover upload (not just `cover_url`)               | User instruction at build time. The `cover_url` field still accepts external URLs; upload is an additional path. |
| App renamed Tsundoku → Fic Tally                  | User instruction after the original build (name collision with another app). Old `tsundoku.db` files are detected and migrated in place. |
| Stats page (`/stats`)                             | User instruction — see the relaxed non-goal note above. Counter-level data only. |
| Series grouping (`parent_id`)                     | User instruction — spinoffs/prequels link to a parent; related chips on both detail pages. |
| Bulk import/export + batch JSON API               | User instruction (replacing one-request-per-entry scripting). `/import`, `/export/*`, `/api/series/batch`; duplicates handled by policy with fuzzy warnings. |
| Duplicate detection on add                        | User instruction — fuzzy (Levenshtein + token overlap) warnings with a Save-anyway override; exact matches drive import/API policies. |
| Mobile-responsive layout                          | User instruction — media queries at 900/760/560 px; no JS changes. |
| Alternative titles (`alt_titles`)                 | User instruction — series often have titles in multiple languages; alternative titles are searchable and feed duplicate detection (exact alt-title matches are strong duplicates). |
| Publication status (`pub_status`)                 | User instruction — ongoing/completed/hiatus/cancelled/upcoming initially; v8 renamed the labels to Ongoing/Complete/Hiatus/Canceled, retired `upcoming` (rows cleared once at startup), and made all three dropdown vocabularies user-editable on `/options`. Deliberately separate from the user's reading status; gets its own stats breakdown. |
| Released year (`year`)                            | User instruction — first release year, 1–9999, 0/empty = unknown. Shown on the detail-page byline; carried through import/export/batch API. |
