# HTTP Reference

Every route the server exposes. All mutations use the Post/Redirect/Get
pattern — they respond with `303 See Other` and a `Location` header
pointing at a GET URL.

Conventions:

- Path wildcards (`{id}`) capture one path segment. IDs are
  slug-formatted (lowercase alphanumeric + dashes), produced by
  `slugify()` in `app.go`.
- All form fields are URL-encoded text unless noted as multipart.
- All HTML responses set `Content-Type: text/html; charset=utf-8`.
- 500 responses render `templates/error.html` with a brief explanation
  of where the error occurred and the error message.

## Library

### `GET /`

Library grid. Accepts five optional query parameters that filter and
sort the list:

| Param   | Values                                                          | Default        |
|---------|-----------------------------------------------------------------|----------------|
| `q`     | Whitespace-separated terms, each matched against title, alt titles, author, tags, description; `#tag` tokens filter by tag | (empty)        |
| `status`| Current reading-status VALUES (default: `reading`, `plan to read`, `on hold`, `dropped`, `completed` — editable on `/options`) | (empty = all)  |
| `type`   | Current type values (default: `manga`, `manhwa`, `manhua`, `light novel`, `web novel` — editable on `/options`) | (empty = all)  |
| `tag`    | Comma-separated tag list with **AND** semantics (case-insensitive): `?tag=isekai,romance` = carries both tags | (empty = all)  |
| `sort`   | `last_read`, `title`, `rating`, `updated`                       | saved default (`library.sort` setting), else `updated` |

Search supports two token kinds, split on whitespace:

- `#tag` tokens — tag filters: `?q=#isekai+#romance` shows only series
  carrying **both** tags (same AND semantics as `?tag=`);
- plain tokens — free-text terms, **all** of which must match, so
  `?q=iron+wren` finds "Iron Tide" by J. Wren without the exact phrase.

Every active tag — from `?tag=` and from `#` tokens — is AND-ed together
and rendered as a removable chip under the toolbar (a plain link that
keeps the remaining filters, so removal works without JavaScript). In
the details layout the mini-tags on cards are clickable toggles that
add/remove tags in the `?tag=` list. With filters active and nothing
matching, the empty state offers a **Clear filters** link.

Next to the Sort select sits a star button (★): clicking it saves the
currently selected sorting as the **default sort** (Settings API
`library` group — server-side, so it applies in every browser). The
star is gold when the current sorting already is the default; an
explicit `?sort=` always wins over the saved default, an invalid one
falls back to it.

Above the grid sit two server-rendered convenience rows:

- **Shelves** — saved views (see `POST /shelves/save`): chips linking to
  their pinned filter+sort URLs; the chip whose canonical params exactly
  reproduce the current view (including the effective sort) is
  highlighted. A native `<details>` popover holds the save form and
  per-shelf delete buttons — no JavaScript involved.
- **Bulk bar** — hidden behind the **Select multiple** toggle in the
  viewbar (v8; the checkboxes used to sit on every cover full-time).
  The toggle is a `<label>` for a nameless checkbox inside the bulk form;
  pure CSS `:has()` reveals the per-card checkboxes and the action bar
  while it is checked. Each card carries its checkbox as a *sibling* of
  the card link (so ticking never opens the series); picking a status
  and hitting **Apply** POSTs every checked `series_ids` to
  `/bulk/status`. JS only live-updates the "N selected" hint, and the
  mode resets on the next page load (PRG back after applying re-hides
  it).

Cards show the chapter line as `current / total · N%` (the percentage
only when the total is known and progress > 0), and the relative "last
read" time carries the exact date-and-time in a tooltip.

Filtering and sorting happen in Go over the full `Store.List()` result.
The URL is bookmarkable. The JavaScript in `static/js/app.js` auto-submits
the filter form on `<select>` change only; the search box submits when
the user presses Enter, clicks the **Search** button, or clicks the **×**
clear button (shown only when a query is active). Per-keystroke
auto-submit was removed — on large libraries it made the page noticeably
sluggish.

The library page also renders (no server round-trip to switch): a **layout
switch** (default / compact / details — the extra fields shown in
details layout are present in every card's DOM and toggled by CSS via
`<html data-layout="…">`), the **Bookmark style** popover that
configures the progress ribbon (color, transparency, width, shape,
side), the **Emblem style** popover (show/hide, seal/check/star, color,
size, transparency, corner), and the **completion emblem** (gold seal on
a cover whose reading status AND publication status are both
`completed`). All of these preferences are stored **server-side** in the
`settings` table (see [Settings API](#settings-api)) — they follow the
database, so every browser/device renders the same look. The server
renders them onto each page as `data-*` attributes on `<html>` plus a
`#ft-settings` JSON blob; an inline script in `layout.html` turns the
blob into CSS custom properties pre-paint. Browsers upgrading from
localStorage-based builds migrate their old prefs automatically on
first load.

**Status codes**

- `200` — always, even if the result set is empty (renders the empty
  state).
- `404` — if the path is not exactly `/`.

## Series detail

### `GET /series/{id}`

Series detail page. Renders `templates/detail.html` with the
joined `EntryWithSeries` plus precomputed view-model fields:

- `CoverSrc` — URL to use in the `<img>` tag, or `""` to fall back to
  the initial-letter placeholder.
- `ProgressPct` — integer 0–100, computed from `current_chapter_number`
  / `total_chapters`. 0 if either is nil or non-positive.
- `ChDisplay` — chapter label for display (`"142"`, `"Extra 1"`,
  `"Vol. 4 Ch. 2"`), or the formatted number if label is empty.
- `TotalDisplay` — `formatChapterNumber(total)` + `"+"` if
  `total_is_known` is false, else just the number. `"—"` if
  `total_chapters` is nil.
- `LastReadRel` — relative-time string like `"2d ago"`, computed
  against `last_read_at`. `"—"` if never read.
- `LastReadAbs` — the exact `"Jan 2, 2006 15:04"` timestamp behind
  `LastReadRel`, rendered as the tooltip on the progress-meta line.
- `Log` — the series' reading history (newest first, capped at 20
  rows): each entry has a date (`"Aug 28"`), the chapter display value
  and a signed delta (`"+1"`, `"−2"`). Rendered in a collapsed
  `<details>` at the bottom of the page.
- `WeekChapters` — count of chapters read in the last 7 days (sum of
  positive log deltas), shown in the history summary.
- `PaceRate` / `PaceDate` — the finish-date estimate
  (`"4.3 ch/wk"` / `"Nov 2026"`), only populated when the total is
  known, current is below total, and there's positive progress on 2+
  distinct days within the last 14 days.
- `AllStatuses`, `AllTypes` — for the `<select>` dropdowns in the
  tracking form.
- `HasCover` — bool; whether `cover_url` is set.

The layout places the Continue-reading CTA + chapter counter at the
top, with description / tags / tracking controls below. This is per
spec, and the inverse of the mockup's order.

**Status codes**

- `200` — series found.
- `404` — series not found.

### `POST /series/{id}/progress`

Update the chapter number on the entry. Three submit modes, dispatched
by the form button name:

| Button name     | Action                                              | Side effect on `last_read_at`     |
|-----------------|-----------------------------------------------------|------------------------------------|
| `btn_plus`      | Advance `current_chapter_number` by 1 (or 1 if nil) | Bumps `last_read_at` to now        |
| `btn_set`       | Set `current_chapter_number` to the typed value      | Bumps `last_read_at` only if new value > old value |
| `btn_clear_num` | Set `current_chapter_number` to nil                  | No bump                            |

The `chapter_label` field is a hidden input on the progress form. If
the label is empty after the change, it's set to the new numeric value
formatted via `formatChapterNumber`. (To set a non-numeric label like
`"Extra 1"`, use the entry-edit form below.)

**Reading history**: whenever the numeric chapter actually changes
(+1, Set to a different value, Clear num), a row is appended to the
`chapter_log` table with the new position, its label, and the signed
delta vs the previous position — this powers the per-series history,
"chapters this week" and the finish-date estimate on the detail page.
No-op updates (re-setting the same value) log nothing; imports and the
batch API never write log rows. A logging failure is logged but
doesn't fail the request.

**Redirect**: `303` → `/series/{id}`.

**Status codes**

- `303` — progress updated, redirect to detail page.
- `404` — series not found.
- `500` — store error.

### `POST /series/{id}/entry`

Update Entry fields: `status`, `rating`, `notes`, `bookmark_label`,
`bookmark_url`. Series fields are untouched. Used by the "Your
tracking" form at the bottom of the detail page.

Form fields:

| Field             | Type        | Notes                                                      |
|-------------------|-------------|------------------------------------------------------------|
| `status`          | select      | One of the five status values; defaults to `plan to read`. |
| `rating`          | number 1–10 | Empty string clears the rating (sets to nil).              |
| `notes`           | textarea    | Free text.                                                 |
| `bookmark_label`  | text        | e.g. `"Chapter 143"`.                                       |
| `bookmark_url`    | url         | Where the Continue-reading button links.                  |

`last_read_at` is **not** bumped — this form is for metadata edits,
not chapter advancement. `updated_at` is bumped (always, on any Save).

**Redirect**: `303` → `/series/{id}`.

**Status codes**

- `303` — entry updated, redirect to detail page.
- `404` — series not found.
- `500` — store error.

## Series metadata edit (the Series record, not the Entry)

### `GET /series/new`

Render the add-series form (`templates/edit.html` with `IsNew=true`).
The form's action is `POST /series/new`.

### `POST /series/new`

Create a new series + its entry. The `title` field is required (used to
derive the series ID via `slugify`). On success: `303` →
`/series/{id}`. On validation failure: `400` with an error message.

Form fields:

| Field             | Type        | Notes                                                              |
|-------------------|-------------|--------------------------------------------------------------------|
| `title`           | text (req)  | Slugified to produce the series ID.                               |
| `alt_titles`      | textarea    | One alternative title per line (also accepts `;` or `|` separators). Stored as a JSON array; searched and duplicate-checked alongside the main title. |
| `type`            | select      | One of the current type values (defaults: manga / manhwa / manhua / light novel / web novel; editable on `/options`). Empty → first type option. |
| `author`          | text        |                                                                    |
| `year`            | number      | First release year, 1–9999. Empty = unknown (0). Bad value → `400`. |
| `pub_status`      | select      | Publication status VALUE: defaults `ongoing`, `completed`, `hiatus`, `cancelled` (labels Ongoing / Complete / Hiatus / Canceled; editable on `/options`); empty = unknown. Bad value → `400`. Separate from the reading `status`. |
| `description`     | textarea    |                                                                    |
| `tags`            | text CSV    | Comma-separated; whitespace trimmed per tag.                      |
| `total_chapters`  | number      | Empty = unknown (stored as nil).                                  |
| `total_is_known`  | checkbox    | Present = true. Unchecked = ongoing series, UI shows `"210+"`.    |
| `source_url`      | url         | Reference URL; not scraped.                                       |
| `cover_url`       | url         | Optional. Must be `http(s)://…` or `/static/covers/…`; other schemes → `400`. |
| `parent_id`       | select      | Optional parent series (grouping). Must exist and ≠ self; else `400`. |
| `status`          | select      | Initial status for the entry. Defaults to `plan to read`.         |
| `chapter_num`     | number      | Initial chapter number (optional).                                |
| `chapter_label`   | text        | Initial chapter label.                                             |
| `rating`          | number 1–10 | Initial rating.                                                    |
| `notes`           | textarea    | Initial notes.                                                     |
| `bookmark_url`    | url         | Initial bookmark.                                                  |
| `bookmark_label`  | text        | Initial bookmark label.                                            |
| `dup_confirm`     | hidden/1    | Set by the "Save anyway" button after a duplicate warning.        |

**Duplicate check.** Before saving, the title **and any alternative
titles** are checked against the library's titles *and* alternative
titles with `findDuplicates` (see `dedup.go`): normalized-exact
matches on any title/alt-title pair (strong — reported as "exact title
match" or "matches alternative title \"…\""), Levenshtein
similarity ≥ 0.8 ("Iorn Tide"), or significant-token overlap ≥ 0.5
("Yona of the Dawn" vs "Akatsuki no Yona"). On a hit the form
re-renders with `200` + a warning box listing candidates (each with
the match reason) and a **Save anyway** button that resubmits with
`dup_confirm=1`; any other submit button then creates the series
normally (`303`).

### `GET /series/{id}/edit`

Render the edit-series-metadata form (`templates/edit.html` with
`IsNew=false`). The form's action is `POST /series/{id}/edit`. Also
shows the cover-upload form (since cover management belongs here, not
on the read-oriented detail page).

### `POST /series/{id}/edit`

Update Series fields only. The Entry row is preserved as-is, including
its `last_read_at` and `updated_at` — this form is for bibliographic
metadata, not tracking. Form fields are the same as `/series/new` minus
the entry-tracking fields (`status`, `rating`, `notes`, `bookmark_*`,
`chapter_*`).

The `created_at` field is round-tripped via a hidden input so existing
records keep their original creation timestamp.

**Redirect**: `303` → `/series/{id}`.

### `POST /series/{id}/cover`

Upload a cover image. Multipart form, single file field named `cover`.
The server sniffs content type from the first 512 bytes (ignoring the
client's `Content-Type` header and filename) and accepts only
`image/png`, `image/jpeg`, `image/gif`, `image/webp`. The destination
filename is `<series_id>.<ext>` — derived from the slug-formatted
series ID, never from the user-supplied filename, so it's
path-injection-safe.

The Series row's `cover_url` is updated to `/static/covers/<filename>`.
The Entry row is untouched. A hidden `cover_url` field on the metadata
edit form ensures subsequent metadata saves don't wipe the URL.

**Redirect**: `303` → `/series/{id}/edit`.

**Status codes**

- `303` — upload succeeded.
- `400` — no file field, or malformed multipart body. Body text
  distinguishes the two ("no file uploaded (expected a 'cover' field)"
  vs "upload malformed: <reason>").
- `413` — body over 8 MiB. Message: "cover image exceeds 8 MiB;
  please use a smaller image". The limit is enforced by
  `http.MaxBytesReader` *before* multipart parsing, so oversized
  uploads fail fast without spilling to disk.
- `415` — content type not an image. Message includes the detected
  type, e.g. "uploaded file is not an image (detected
  application/octet-stream)".
- `404` — series not found.
- `500` — disk error or store error.

### `POST /series/{id}/cover/url`

Set the cover from a remote URL instead of an uploaded file. Form fields:

| Field       | Values                          | Notes                                          |
|-------------|---------------------------------|------------------------------------------------|
| `cover_url` | `http://…`, `https://…`, or `/static/covers/…` | Validated server-side; anything else (notably `javascript:`/`data:`) is rejected. |

If the previous cover was an uploaded file, that file is removed from
disk (no orphans). Writing the metadata form's hidden `cover_url` field is
unaffected — both paths write the same `Series.cover_url` column.

**Redirect**: `303` → `/series/{id}/edit`.

**Status codes**: `303` ok · `400` invalid URL scheme · `404` unknown series · `500` store error.

### `POST /series/{id}/cover/delete`

Remove the cover image file from disk (best-effort; an orphan is
harmless) and clear `cover_url` on the Series row.

**Redirect**: `303` → `/series/{id}/edit`.

### `POST /series/{id}/delete`

Delete the series, its entry, and its cover image file. Idempotent —
deleting an unknown ID is a no-op (the spec says no error, just for
safety). The cover file is removed best-effort. Any series whose
`parent_id` pointed at the deleted series is detached (its `parent_id`
is cleared), never deleted.

**Redirect**: `303` → `/`.

## Reading stats

### `GET /stats`

Server-rendered dashboard. No params. Computes from `List()` +
`ReadDays()` in Go (no aggregate SQL):

- **Headline cards** — currently reading; completed this month (via
  `entry.completed_at`, set on the transition *into* `completed`);
  average rating (mean of non-null ratings, one decimal); current
  reading streak + longest streak (from the `daily_reads` table — a
  "reading day" is a UTC date with ≥ 1 chapter advance; the current
  streak may end today *or yesterday*, so it isn't broken until a full
  day passes).
- **Secondary line** — total series, read this week / last 30 days
  (by `last_read_at`), plan/on-hold/dropped counts, chapters tracked
  (sum of `current_chapter_number`).
- **Breakdowns** — status bars (in enum order), type bars, top-10 tags.
- **Activity strip** — one bar per day for the last 30 days; height is
  the day's chapter-update count relative to the window's max.
- **Recently completed** — last 5 by `completed_at`, newest first.

## Batch import

### `GET /import`

Renders the import page: file upload (`.csv`/`.json`), paste textarea,
duplicate-policy select (`skip` / `update` / `create`), and a `dry_run`
checkbox.

### `POST /import`

Accepts `multipart/form-data` (file field `file` wins when present) or
urlencoded (textarea field `payload`). CSV must be header-addressed and
contain a `title` column; JSON may be the export envelope
(`{"series":[…]}`) or a bare array. Auto-detected from the first
non-space byte. Body cap 8 MiB, max 1000 rows.

**Duplicate policy semantics** (shared with the batch API):

- `skip` — exact duplicates (normalized-title match on any title /
  alternative-title pair, or same id) are skipped; fuzzy near-matches
  are imported with an annotation.
- `update` — exact duplicates are overwritten (their `created_at` is
  preserved).
- `create` — always creates; a colliding slug gets `-2`, `-3`, … suffix.

CSV columns (header, any subset): `title, alt_titles, type, author,
year, pub_status, description, tags, source_url, cover_url, parent_id,
total_chapters, total_is_known, status, chapter_num, chapter_label,
rating, notes, bookmark_url, bookmark_label`. Tags split on `,` `;` `|`.
Alternative titles split on `;` `|` / newline (**not** commas — titles
  themselves often contain commas). `pub_status` is one of the current
publication-status VALUES (`ongoing / completed / hiatus / cancelled` by
  default — see `/options`; bad values fail the row).
`year` must be 1–9999. `total_is_known` accepts `true/1/yes/on`.

In JSON payloads, `alt_titles` may be an array
(`"alt_titles": ["A", "B"]`) or a single string
(`"alt_titles": "A; B"`); `year` is a number; `pub_status` is a string.

**Response**: re-rendered import page with a per-row results table
(created / updated / skipped / error + message) and summary counts.

## Export

### `GET /export/json`

Downloads the whole library as `fic-tally-export-YYYYMMDD.json`:

```json
{ "generator": "fic-tally", "exported_at": "…", "series": [ …joined EntryWithSeries rows… ] }
```

Round-trips through `POST /import` (policy `update` restores in place)
and `POST /api/series/batch` unchanged.

### `GET /export/csv`

Downloads `fic-tally-export-YYYYMMDD.csv` with the canonical column
order above (RFC 4180; quoted fields where needed).

## Batch API

### `POST /api/series/batch`

JSON in, JSON out. One HTTP request + one SQLite transaction for up to
**1000 entries** (`Store.SaveAll`) — the efficient replacement for
one-request-per-entry scripting.

Request (either shape):

```json
{"series": [ {"title": "…", "alt_titles": ["Yona of the Dawn"], "type": "manga",
              "status": "reading", "pub_status": "ongoing", "year": 2009,
              "current_chapter_number": 5, "rating": 8, "tags": ["…"],
              "cover_url": "https://…", "parent_id": "…", …} ],
 "duplicate_policy": "skip", "dry_run": false}
```

- Field names match the JSON export (`chapter_num` /
  `chapter_number` accepted as aliases). Defaults: `type=manga`,
  `status=plan to read`.
- `alt_titles` accepts an array or a `";"`-separated string; `pub_status`
  must be one of the current publication-status VALUES (`ongoing /
  completed / hiatus / cancelled` by default — see `/options`; or omitted);
  `year` must be 1–9999 (or omitted).
- `duplicate_policy`: `skip` (default) / `update` / `create` — same
  semantics as `POST /import` (exact matches consider titles AND
  alternative titles on both sides).
- `dry_run: true` validates + resolves everything but writes nothing.

Response `200`:

```json
{"created": 1, "updated": 0, "skipped": 1, "failed": 0, "dry_run": false,
 "results": [{"index": 0, "title": "…", "id": "…", "action": "created",
              "message": ""}]}
```

Per-item validation failures don't abort the batch — they're reported
with `action: "error"` and a `message`; the rest of the batch commits.
**Status codes**: `200` batch processed (even with per-item failures) ·
`400` malformed JSON / bad policy / missing `series` / over 1000 items ·
`500` transaction failure (whole batch rolled back).

## Settings API

Server-side UI preferences. Every configurable look-and-feel knob —
library `layout`, progress `ribbon`, completion `emblem`, `theme`, and
the library's saved `default sort` — lives in the server's SQLite
`settings` table (one canonical-JSON row per group), so preferences
follow the database rather than the browser.

### `GET /api/settings`

Returns the stored preference groups. Absent groups are omitted from
the response; the client falls back to built-in defaults for them.

```json
{"layout": "compact",
 "ribbon": {"color": "#5b7fbd", "opacity": 1, "width": 11, "shape": "tag", "side": "left"},
 "emblem": {"show": "on", "style": "seal", "color": "", "size": 26, "opacity": 1, "pos": "br"},
 "theme": "dark",
 "library": {"sort": "updated"},
 "shelves": [{"name": "Reading now", "params": "sort=updated&status=reading"}]}
```

### `POST /api/settings`

Upserts preference groups. The body is parsed as JSON regardless of
`Content-Type` (the browser's `sendBeacon` flush sends `text/plain`).
Present groups are replaced wholesale; absent groups are untouched;
fields omitted from a group keep the server-side defaults
(canonicalization). The response is the resulting settings, same shape
as `GET`.

Validation (all violations → `400` with a descriptive message):

| Group    | Field     | Allowed values                                  |
|----------|-----------|--------------------------------------------------|
| `layout` |           | `default`, `compact`, `details`                  |
| `theme`  |           | `light`, `dark`                                  |
| `ribbon` | `color`   | `""` (theme crimson) or `#rrggbb`                |
|          | `opacity` | 0.15 – 1                                         |
|          | `width`   | 3 – 20 (px)                                      |
|          | `shape`   | `tag`, `line`, `triangle`, `round`               |
|          | `side`    | `left`, `right`                                  |
| `emblem` | `show`    | `on`, `off`                                      |
|          | `style`   | `seal`, `check`, `star`                          |
|          | `color`   | `""` (gold) or `#rrggbb`                         |
|          | `size`    | 16 – 40 (px)                                     |
|          | `opacity` | 0.15 – 1                                         |
|          | `pos`     | `tl`, `tr`, `bl`, `br`                           |
| `library`| `sort`    | `last_read`, `title`, `rating`, `updated` — the default sort applied whenever `/` loads without an explicit `?sort=` |
| `shelves`| (array)   | `[{"name", "params"}]` — max 12 entries; `name` 1–40 chars (unique); `params` a canonical query string built only from `q` / `status` / `type` / `tag` / `sort` with valid enum values (see POST /shelves/save) |

Unknown top-level groups are rejected. Requests carrying an `Origin`
header whose host doesn't match the request host get `403` — this
blocks drive-by cross-site POSTs from malicious web pages while
curl/scripts (no `Origin`) keep working.

**Status codes**: `200` stored (body = resulting settings) · `400`
invalid payload · `403` cross-origin · `500` store failure.

Client flow (`static/js/app.js`): changes are applied immediately in
the DOM and persisted with a 250 ms debounce (one request per slider
gesture, not one per pixel); a `pagehide` beacon flushes pending
changes if the tab closes mid-debounce. On first load after upgrading
from a localStorage build, legacy prefs are pushed to the server once
and the localStorage keys are deleted.

## Theme

### `POST /theme`

Toggle the theme. Form fields:

| Field   | Values          | Notes                                            |
|---------|-----------------|--------------------------------------------------|
| `theme` | `dark`, `light` | Any other value falls back to `dark`.            |
| `back`  | path            | Where to redirect after saving. Must start with `/`; otherwise ignored and the user is sent to `/`. |

The choice is stored as a **server-side setting** (per-server, not a
per-browser cookie as in earlier builds), and every rendered page gets
`data-theme` on `<html>` from the stored value. A legacy `theme`
cookie from an older build is adopted as the server-side setting
automatically on the first render, then ignored.

**Redirect**: `303` → `back` (or `/` if invalid).

## Shelves (saved views)

Shelves pin a filter+sort combination as a named one-click shortcut on
the library page. They are stored as the `shelves` settings group (see
the Settings API above), so they follow the database to every browser.

### `POST /shelves/save`

Saves the posted view under `name`. Form fields (all five filter fields
are rendered as hidden inputs by the library page, carrying the current
view):

| Field    | Values                                       | Notes                                   |
|---------|----------------------------------------------|-----------------------------------------|
| `name`   | 1–40 chars (trimmed)                          | Re-saving an existing name replaces it. |
| `q`      | search text (≤ 200 chars)                     | Empty = dropped.                        |
| `status` | a status enum or empty                        | Invalid → 400.                          |
| `type`   | a type enum or empty                          | Invalid → 400.                          |
| `tag`    | comma list (normalized: trimmed, empties     | ≤ 200 chars after normalization.        |
|         | dropped)                                     |                                         |
| `sort`   | a sort enum or empty                          | The library form posts the EFFECTIVE    |
|         |                                              | sort, so shelves pin sorting too.       |

The params are canonicalized (only known keys, valid enums, `Encode()`
sorted). A view that canonicalizes to nothing (all fields empty) is
rejected with `400 "nothing to save"`; the shelf list is capped at 12
(`400` when full, unless replacing an existing name).

**Redirect**: `303` → `/?<canonical params>` (the saved view itself).

### `POST /shelves/delete`

Removes the shelf with the posted `name` (exact match). Unknown names
are a no-op (still `303`). `back` (optional, must start with `?`)
returns the user to the current view, e.g. `?status=reading`.

**Redirect**: `303` → `/?back` or `/`.

## Bulk status

### `POST /bulk/status`

Applies one status to every selected series — the bulk action bar on
the library page. Form fields:

| Field         | Values            | Notes                                     |
|---------------|-------------------|-------------------------------------------|
| `series_ids`  | repeated series id| One value per checked card checkbox.      |
| `status`      | status enum       | Empty/invalid → `400`.                    |
| `back`        | query string      | Must start with `?`; where to redirect.   |

Unknown ids are skipped (e.g. deleted in another tab). Each series is
saved through the normal Store path, so `completed_at` transitions and
`updated_at` bumps behave exactly like a single-series status edit.
Empty selection is a harmless no-op redirect.

**Redirect**: `303` → `/?back` (or `/`).

## Dropdown options

The reading-status, type and publication-status vocabularies are
user-editable (stored in the `options` settings group). Every dropdown,
validation, filter and stats breakdown reads the same live list, so a
value is valid everywhere or nowhere. Each option is a `{value, label}`
pair: the **value** is the permanent ID used by the database, URLs, CSV/
JSON import and shelves; the **label** is display-only and freely
renamable — renaming never touches stored data.

### `GET /options`

Renders the editor (`templates/options.html`): three sections
(Reading status / Type / Publication status), one row per option with
the value in mono, a label input, a position input (dropdown order), and
— where allowed — a remove checkbox. Built-ins the app relies on (the
five reading statuses: stats tiles, `completed_at` transitions, the
new-series default; publication `completed`: the fully-completed
emblem) are shown as *built-in* and cannot be removed; options still
used by series show an "N in use" badge (removal pre-disabled). Usage
counts come from three `GROUP BY` queries per render. `?saved=1` (the
PRG target) shows a confirmation banner.

### `POST /options/save`

Applies renames, additions, removals and reordering in ONE POST; any
problem rejects the whole save with `400` + a plain-language message
(option lists are load-bearing — a partial apply would be worse than a
retry). Form fields (keyed by the immutable value; absent fields keep
the stored value, so a stale form can't wipe a just-added option):

| Field                          | Notes                                                       |
|--------------------------------|-------------------------------------------------------------|
| `label_{list}_{value}`         | New label, 1–40 chars after trimming. Empty → `400`.        |
| `pos_{list}_{value}`           | 1–99; the select order is the ascending sort (stable).      |
| `del_{list}_{value}`           | `1` = remove. Protected → `400`; still in use → `400` with  |
|                                | the count and a hint to reassign first.                     |
| `add_{list}`                   | New option label (≤ 40 chars); its value becomes the        |
|                                | lowercased label, appended. Duplicates (value or label,     |
|                                | case-insensitive) → `400`. Max 20 options per list.         |

`{list}` is `status`, `type` or `pub_status`. Note that values may
contain spaces (`label_status_plan to read`) — form names are legal
with spaces; URL-encode them in tests.

The `options` group is deliberately absent from `POST /api/settings`
(unknown-group rejection) — it can only be written through this
validated endpoint.

**Redirect**: `303` → `/options?saved=1`.

**Startup behavior** (`initOptions`): a missing or invalid `options`
group is seeded with the defaults. Seeding also runs the one-time data
migration on the absent-group path: `pub_status='upcoming'` rows are
cleared to `""` (unknown) — the value no longer exists in the
vocabulary. Values `ongoing/completed/hiatus/cancelled` keep their
spelling; only labels changed in v8.

## Full backup

### `GET /backup`

Downloads a zip containing everything the JSON export doesn't:

| Entry         | Contents                                                       |
|---------------|----------------------------------------------------------------|
| `fic-tally.db`| Consistent snapshot of the whole database (`VACUUM INTO`:     |
|               | series, entries, settings incl. shelves, daily_reads streak   |
|               | counters, chapter_log reading history).                       |
| `covers/*`    | Every uploaded cover image, flat `<series-id>.<ext>` names.    |
| `RESTORE.txt` | What the archive is and how to restore it (stop server,       |
|               | replace the db file, copy covers/ into static/covers/).       |

Headers: `Content-Type: application/zip`, `Content-Disposition:
attachment; filename="fic-tally-backup-YYYYMMDD-HHMMSS.zip"`. The zip
is streamed (never buffered whole in memory); the DB snapshot is
written to a temp file first because `VACUUM INTO` refuses to overwrite
an existing path.

**Status codes**: `200` streaming the zip · `500` snapshot failure.

## Static assets

### `GET /static/...`

Serves files from the directory passed via `-static` (default
`static`). `http.StripPrefix` removes `/static/` from the URL before
resolving the path. `http.FileServer` handles the response, including
`Content-Type` detection (sniffed from file extension via
`mime.TypeByExtension`).

The whole server is wrapped in a `noCache` middleware (`app.go`) that
sets `Cache-Control: no-cache` on **every** response, static files and
rendered pages alike. The browser may keep a cached copy but must
revalidate (`If-Modified-Since`) before reusing it; unchanged files
come back as cheap `304`s. Without this header browsers fall back to
heuristic caching and can serve days-old `app.js`/`app.css` after an
upgrade — new HTML + old assets looks like a broken app (unstyled cards,
dead buttons, resurrected live search).

Asset URLs additionally carry a `?v=` cache-buster (see
`templates/layout.html`): after upgrading, the changed asset gets a new
URL that can never match an already-cached entry, so even a browser
holding a stale pre-`no-cache` copy fetches the fresh file without a
hard refresh. Bump the `v=` values whenever you change `app.css` or
`app.js`.

Known asset paths (the `?v=` suffix is a cache-buster, not part of the
path):

- `/static/css/app.css` — the stylesheet (referenced as `app.css?v=8`).
- `/static/js/app.js` — the script (referenced as `app.js?v=7`).
- `/static/manifest.json` — the PWA web manifest (name, icons,
  `display: standalone`, theme color) linked from every page; named
  `.json` so `mime.TypeByExtension` serves `application/json`, which
  browsers accept for manifests.
- `/static/img/icon.png`, `/static/img/apple-touch-icon.png` — the app
  icon (favicon, topbar logo, home-screen/shortcut icon).
- `/static/covers/<id>.<ext>` — uploaded cover images.

Directory listings are not disabled — `http.FileServer` returns an HTML
listing for directory paths. For a single-user localhost app this is
acceptable; if you ever bind to a LAN, consider wrapping the file
server with `disableDirectoryListing = true`.

## Error handling

`app.serverError(w, where, err)` is the single error path for store
failures. It:

1. Logs `[error] <where>: <err>` to stderr.
2. Sets `500 Internal Server Error` on the response.
3. Renders `templates/error.html` with `Where` and `Err` for display.

Validation errors (bad input) are returned as `400 Bad Request` with
plain text body. `404 Not Found` uses `http.NotFound`, which serves a
minimal HTML page.
