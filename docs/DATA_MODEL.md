# Data Model

The data model is two entities kept structurally separate on purpose:
**Series** (bibliographic metadata about a work) and **Entry** (the
user's personal tracking data for that work). The two are joined at
read time by `series_id`; the `EntryWithSeries` struct exists only for
the join — persistence keeps the two on separate tables so a metadata
refresh can blow away and rewrite the Series row without losing the
Entry.

This separation is the spec's most insistent design decision. The
comment in `models.go` calls it out explicitly:

> Two entities kept separate on purpose, refreshing a series' metadata
> later shouldn't touch your personal tracking data.

## Series

Bibliographic metadata. Stored in the `series` table.

| Field              | Go type        | SQLite column        | Notes                                                |
|--------------------|----------------|------------------------|------------------------------------------------------|
| `ID`               | `string`       | `id TEXT PRIMARY KEY`  | Slug of the title. Stable across edits.              |
| `Title`            | `string`       | `title TEXT NOT NULL`   | Free text.                                            |
| `AltTitles`        | `[]string`     | `alt_titles TEXT NOT NULL DEFAULT '[]'` | JSON-encoded array of alternative/translated titles. Included in library search and duplicate detection (an exact alt-title hit is a *strong* duplicate). Forms accept one title per line; CSV/JSON import splits on `;`/`|`/newline (deliberately not commas — titles contain commas). |
| `Type`             | `SeriesType`   | `type TEXT NOT NULL`    | One of the five enumerated type values.              |
| `Author`           | `string`       | `author TEXT NOT NULL DEFAULT ''` | Free text. Empty string is allowed.        |
| `Year`             | `int`          | `year INTEGER NOT NULL DEFAULT 0` | First release year. `0` = unknown (omitted from JSON export). Validated 1–9999 on input. |
| `PubStatus`        | `PubStatus`    | `pub_status TEXT NOT NULL DEFAULT ''` | PUBLICATION status of the work itself — VALUE one of `ongoing` / `completed` / `hiatus` / `cancelled` (labels Ongoing / Complete / Hiatus / Canceled; editable on `/options`). Empty = unknown. Deliberately separate from the user's reading `Status`: a series you finished can still be an ongoing publication. |
| `Description`      | `string`       | `description TEXT NOT NULL DEFAULT ''` | Free text.                          |
| `CoverURL`         | `string`       | `cover_url TEXT NOT NULL DEFAULT ''` | Either `/static/covers/<id>.<ext>` for an uploaded cover, an external URL, or empty (placeholder). |
| `Tags`             | `[]string`     | `tags TEXT NOT NULL DEFAULT '[]'`  | JSON-encoded array of strings.              |
| `SourceURL`        | `string`       | `source_url TEXT NOT NULL DEFAULT ''` | Reference URL; not scraped.               |
| `ParentID`         | `string`       | `parent_id TEXT NOT NULL DEFAULT ''` | Series grouping: slug of the parent series (spinoff/prequel linking). Empty = standalone. Soft reference — no FK; deleting a parent clears children's `parent_id`. |
| `TotalChapters`    | `*float64`     | `total_chapters REAL`   | Nullable. `nil` = unknown (UI shows `—`).           |
| `TotalIsKnown`     | `bool`         | `total_is_known INTEGER NOT NULL DEFAULT 0` | `false` for an ongoing series; UI shows `210+`. |
| `CreatedAt`        | `time.Time`    | `created_at TEXT NOT NULL` | RFC3339 string.                          |

## Entry

Personal tracking data. Stored in the `entry` table. One row per
Series (1:1 by `series_id`).

| Field                  | Go type     | SQLite column                          | Notes                                                            |
|------------------------|-------------|------------------------------------------|------------------------------------------------------------------|
| `SeriesID`             | `string`    | `series_id TEXT PRIMARY KEY`              | Foreign key to `series(id)` with `ON DELETE CASCADE`.            |
| `Status`              | `Status`    | `status TEXT NOT NULL DEFAULT 'plan to read'` | One of the five status values.                              |
| `CurrentChapterNum`   | `*float64`  | `current_chapter_num REAL`                | Nullable; drives the progress calculation.                      |
| `CurrentChapterLabel` | `string`    | `current_chapter_label TEXT NOT NULL DEFAULT ''` | Always populated; what's actually displayed.             |
| `Rating`               | `*int`      | `rating INTEGER`                          | Nullable, 1–10 when set.                                         |
| `Notes`               | `string`    | `notes TEXT NOT NULL DEFAULT ''`          | Free text.                                                       |
| `BookmarkURL`         | `string`    | `bookmark_url TEXT NOT NULL DEFAULT ''`  | Where the Continue-reading button links.                        |
| `BookmarkLabel`       | `string`    | `bookmark_label TEXT NOT NULL DEFAULT ''` | e.g. `"Chapter 143"`. Shows as `Continue reading → Chapter 143`. |
| `UpdatedAt`           | `time.Time` | `updated_at TEXT NOT NULL`                | Bumps on ANY save (notes, rating, metadata refresh, etc.).       |
| `LastReadAt`           | `time.Time` | `last_read_at TEXT NOT NULL`              | Bumps ONLY when `current_chapter_number` actually advances.      |
| `CompletedAt`         | `time.Time` | `completed_at TEXT NOT NULL DEFAULT ''`   | Set when `status` transitions INTO `completed`; cleared on transition out; empty otherwise. Drives the stats page's "completed this month". Managed inside `saveTx` so every write path (form, progress, API, import) behaves identically. |

## daily_reads (stats support table)

One row per UTC day with at least one chapter advance. Written by
`saveTx` whenever a save advances progress
(`INSERT … ON CONFLICT(date) DO UPDATE SET count = count + 1`).

| Column  | Type     | Notes                                       |
|---------|----------|---------------------------------------------|
| `date`  | `TEXT`   | Primary key; UTC `"2006-01-02"`.            |
| `count` | `INTEGER`| Number of chapter-advances logged that day. |

Why a table instead of deriving history from `last_read_at`:
`last_read_at` stores only each series' most recent read. Reading the
same series two days in a row overwrites yesterday's evidence, so
streaks and the 30-day activity strip would silently undercount. The
counter rows contain no per-series detail — that's what keeps this from
turning into reading-history analytics (see SPEC_COMPLIANCE's departure
note).

## chapter_log (per-series reading history)

One row per chapter update, written ONLY by the progress handler when
the numeric chapter actually changes (+1, Set to a different value,
Clear num). Powers the per-series history timeline, the "chapters
this week" figure and the finish-date estimate on the detail page.

| Column       | Type     | Notes                                                    |
|--------------|----------|-----------------------------------------------------------|
| `id`         | `INTEGER`| Primary key, autoincrement — insertion order = timeline.  |
| `series_id`  | `TEXT`   | Soft reference (no FK); rows deleted with their series.  |
| `chapter`    | `REAL`   | Position AFTER the update; `NULL` when cleared.          |
| `label`      | `TEXT`   | Display label after the update (`"142"`, `"Extra 1"`).   |
| `delta`      | `REAL`   | Signed change vs the previous position (0 from nil).    |
| `at`         | `TEXT`   | RFC3339 timestamp.                                        |

Indexed on `(series_id, id)`; reads are capped at 100 rows (the page
shows 20, the pace math 14 days). Unlike `daily_reads`, this table
keeps per-series detail — deliberately: the history view and pace
estimate are per-series questions the global counters can't answer.
Note the semantic difference from `daily_reads`: a Set that jumps
forward by 50 logs ONE `daily_reads` increment and ONE `chapter_log`
row with `delta=50`; "chapters this week" sums positive deltas, so it
counts all 50.

## EntryWithSeries

The joined view the UI consumes. Not a table — a Go struct returned by
`Store.Get` and `Store.List`. The struct embeds both `Series` and
`Entry` so template fields like `.Title`, `.Author`, `.Status`,
`.Rating` work without qualification (Go's field promotion).

## settings (UI preferences table)

One row per preference group — `layout`, `ribbon`, `emblem`, `theme`,
`library`, `shelves`, `options` — stored as canonical JSON so every
browser/device opening the app renders the same look (prefs follow the
database, not the browser).

| Column       | Type   | Notes                                          |
|--------------|--------|------------------------------------------------|
| `key`        | `TEXT` | Primary key; group name.                       |
| `value`      | `TEXT` | Canonical JSON for the group (validated).      |
| `updated_at` | `TEXT` | RFC3339, bumped on every write.                |

Values are validated server-side on write (`parseSettingsPatch` in
`settings.go`): enums, bounded numbers, `#rrggbb` colors only. The
`layout`/`theme` rows hold JSON strings (`"compact"`), `ribbon`/`emblem`
hold JSON objects, `library` holds the saved default sort
(`{"sort":"updated"}`), and `shelves` holds the saved-view list
(`[{"name":"Reading now","params":"sort=updated&status=reading"}]`,
max 12, params canonicalized to known keys/valid enums). Absent rows
mean "use the built-in defaults". See
`docs/HTTP_REFERENCE.md` → Settings API for the field-by-field
validation table.

The `options` group (v8) holds the user-editable dropdown vocabularies:

```json
{
  "status":     [{"value":"reading","label":"Reading"}, …],
  "type":       [{"value":"manga","label":"Manga"}, …],
  "pub_status": [{"value":"ongoing","label":"Ongoing"},
                  {"value":"completed","label":"Complete"}, …]
}
```

It is written ONLY by `POST /options/save` (never the settings API —
unknown-group rejection), re-validated on load (`parseOptionLists`:
non-empty lists ≤ 20 entries, unique non-empty values, and the semantic
anchors `reading`/`plan to read`/`on hold`/`dropped`/`completed` plus
pub `completed` must survive; a bad blob falls back to defaults), and
mirrored into an in-memory copy (`app.opts`, RWMutex) that every
validation and render reads. Seeding the group on the first v8 start
also clears `pub_status='upcoming'` rows to `""` (the retired value) —
the one-time migration.

## Enums

Since v8 the three dropdown vocabularies are **data, not code** — the
live lists live in the `options` settings group (see above) and are
editable on `/options`. The Go constants below remain as the built-in
defaults and the semantic anchors that can never be removed.

### SeriesType

Default values, stored as `TEXT`:

| Constant           | String value     | Default label  |
|--------------------|------------------|----------------|
| `TypeManga`        | `"manga"`        | `Manga`        |
| `TypeManhwa`       | `"manhwa"`        | `Manhwa`       |
| `TypeManhua`       | `"manhua"`        | `Manhua`       |
| `TypeLightNovel`   | `"light novel"`   | `Light novel`  |
| `TypeWebNovel`     | `"web novel"`     | `Web novel`    |

When a form or import row omits `type`, the default is `manga` — or the
first current option if `manga` itself was removed
(`optionLists.defaultType`).

### Status

Default values, stored as `TEXT` using the spec's full canonical strings
(not the abbreviated forms the mockup JS used internally). All five are
protected from removal: `completed` drives the `completed_at`
transition + the fully-completed emblem, `reading` the
Currently-Reading stats tile, `plan to read` the new-series/import
default, and all five the fixed stats tiles:

| Constant           | String value       | Default label     |
|--------------------|---------------------|-------------------|
| `StatusReading`    | `"reading"`         | `Reading`         |
| `StatusPlanToRead` | `"plan to read"`   | `Plan to read`    |
| `StatusOnHold`     | `"on hold"`         | `On hold`         |
| `StatusDropped`    | `"dropped"`         | `Dropped`         |
| `StatusCompleted`  | `"completed"`       | `Completed`       |

Dropdown order and display text come from the option list. The template
function `statusDotClass` maps VALUES to the CSS dot class
(`dot-reading`, …); custom statuses fall back to `dot-plan`. The class
form is used because `html/template`'s CSS-context escaping rejects
inline `var()` interpolation.

### PubStatus (publication status)

Default values, stored as `TEXT`; `pub_status "completed"` is protected
(it drives the fully-completed emblem). v8 renamed the LABELS and
retired the `upcoming` value:

| Constant         | String value     | Default label |
|------------------|------------------|---------------|
| `PubOngoing`     | `"ongoing"`      | `Ongoing`     |
| `PubCompleted`   | `"completed"`    | `Complete`    |
| `PubHiatus`      | `"hiatus"`       | `Hiatus`      |
| `PubCancelled`   | `"cancelled"`    | `Canceled`    |

("Upcoming" existed pre-v8; rows holding it are cleared to `""` once
at startup — see the `options` group above.)

## Design rationale for specific fields

### `current_chapter_number` vs `current_chapter_label`

A bare numeric field breaks the moment a series uses `"Extra 1"` or
`"Vol. 4 Ch. 2"` as a chapter marker. Splitting the two means:

- The ribbon / progress bar always has a number to compute against
  when one exists (the `number` field).
- The UI always has a string to display, whatever the marker's shape
  (the `label` field).
- When the user enters a bare number, the label is auto-set to that
  number formatted as a string (`142` → label `"142"`).
- When the user enters a non-numeric marker (`"Extra 1"`), the
  number is cleared via the "Clear num" button, the label keeps the
  marker, and the ribbon falls back to the last known numeric
  position.

The progress calculation in `progressPct()` returns 0 if
`CurrentChapterNum` is nil, rather than erroring — the ribbon simply
doesn't render until a numeric position is available.

### `total_chapters` + `total_is_known`

An ongoing series doesn't have a real "final chapter," so a bare
`total_chapters=210` silently claims completeness it doesn't have.
The two-field split:

- `total_chapters` — the highest known chapter count (nullable, nil
  means "no idea").
- `total_is_known` — bool, false for an ongoing series. The UI
  appends `+` to the total (`210+`) when this is false, signalling
  "this is the highest known, not the final".

For series where the total is genuinely unknown (no published chapter
count), `total_chapters` is nil and the UI shows `—`.

### `updated_at` vs `last_read_at`

Editing a note or refreshing a cover shouldn't make a series look
"recently read." Only actual chapter progress should touch
`last_read_at`. That distinction is what makes a "recently active"
sort mean anything.

- `updated_at` bumps on **every** save, regardless of what changed.
- `last_read_at` bumps only when `Save` is called with
  `advanceProgress=true` — which happens when the progress form
  advances the chapter (`btn_plus`, or `btn_set` with a value greater
  than the old one).

The default sort on the library page is `sort=last_read`, ordered by
`last_read_at` descending. Entries with `last_read_at` = zero
("never read") sort below all entries with a real timestamp.

### `rating` as nullable 1-10 integer

The mockup used 5 stars, which is the most common rating UI but
limiting — you can't distinguish "fine" from "good." The spec
specifies 1–10 as a plain integer, no stars. Cleaner in a dense list,
and it avoids needing half-star logic. Stored as a nullable `INTEGER`
so "unrated" is a distinct state from "rated 1."

### `tags` as JSON-encoded `TEXT`

SQLite has no native array type. The three common workarounds are:
comma-separated text, a join table, or JSON-encoded text. JSON is the
best fit here because:

- It round-trips cleanly to `[]string` in Go via
  `encoding/json.Unmarshal`.
- It survives tags with commas in them (a comma in a tag would break
  the comma-separated approach).
- It's queryable via SQLite's `JSON_EACH` function if the library
  ever needs SQL-side tag filtering.

The current `tag` filter implementation does in-memory matching
(`for _, t := range e.Tags { if strings.EqualFold(t, tagFilter) ... }`),
so JSON is overkill today — but it costs nothing and keeps the door
open.

## Schema

The schema is created by `migrate()` in `sqlite_store.go` on startup,
with `CREATE TABLE IF NOT EXISTS` so existing DBs are not touched.
Newer columns (`series.parent_id`, `series.alt_titles`, `series.year`,
`series.pub_status`, `entry.completed_at`) and the `daily_reads` table
are added by guarded `ALTER TABLE`/`CREATE TABLE` migrations
(`PRAGMA table_info` checks), so pre-rename databases upgrade in place
on first run:

```sql
CREATE TABLE IF NOT EXISTS series (
  id              TEXT PRIMARY KEY,
  title           TEXT NOT NULL,
  alt_titles      TEXT NOT NULL DEFAULT '[]',
  type            TEXT NOT NULL,
  author          TEXT NOT NULL DEFAULT '',
  year            INTEGER NOT NULL DEFAULT 0,
  pub_status      TEXT NOT NULL DEFAULT '',
  description     TEXT NOT NULL DEFAULT '',
  cover_url       TEXT NOT NULL DEFAULT '',
  tags            TEXT NOT NULL DEFAULT '[]',
  source_url      TEXT NOT NULL DEFAULT '',
  total_chapters  REAL,
  total_is_known  INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);
-- guarded migrations:
--   ALTER TABLE series ADD COLUMN parent_id  TEXT NOT NULL DEFAULT '';
--   ALTER TABLE series ADD COLUMN alt_titles TEXT NOT NULL DEFAULT '[]';
--   ALTER TABLE series ADD COLUMN year       INTEGER NOT NULL DEFAULT 0;
--   ALTER TABLE series ADD COLUMN pub_status TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS entry (
  series_id             TEXT PRIMARY KEY,
  status                TEXT NOT NULL DEFAULT 'plan to read',
  current_chapter_num   REAL,
  current_chapter_label TEXT NOT NULL DEFAULT '',
  rating                INTEGER,
  notes                 TEXT NOT NULL DEFAULT '',
  bookmark_url          TEXT NOT NULL DEFAULT '',
  bookmark_label        TEXT NOT NULL DEFAULT '',
  updated_at            TEXT NOT NULL,
  last_read_at          TEXT NOT NULL,
  FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE
);
-- guarded migration: ALTER TABLE entry ADD COLUMN completed_at TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS daily_reads (
  date  TEXT PRIMARY KEY,   -- UTC "2006-01-02"
  count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,   -- layout | ribbon | emblem | theme | library | shelves | options
  value      TEXT NOT NULL,      -- canonical JSON for the group
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chapter_log (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  series_id TEXT NOT NULL,
  chapter   REAL,             -- position AFTER the update; NULL = cleared
  label     TEXT NOT NULL DEFAULT '',
  delta     REAL NOT NULL DEFAULT 0, -- signed change vs previous position
  at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chapter_log_series ON chapter_log(series_id, id);
```

Two additional PRAGMAs are set on startup:

- `PRAGMA journal_mode=WAL` — write-ahead logging for non-blocking
  reads during writes.
- `PRAGMA foreign_keys=ON` — set via the connection string
  (`_pragma=foreign_keys(1)`), so the `ON DELETE CASCADE` on the
  entry's foreign key actually fires when a series is deleted.

## Seed data

On first run, `seedIfEmpty()` populates the library with two example
series if the `series` table is empty. Both are adapted from the
mockup's sample data:

- **Iron Tide** (manhwa, ongoing) — `total_chapters=210`,
  `total_is_known=false`, current chapter 142, rating 8, bookmark
  label `"Chapter 143"`, alt titles `"Tide of Iron"`/`"Iron Tide
  (Remaster)"`, `pub_status=ongoing`, `year=2019`. Shows the `210+` UI
  treatment, the "Also known as" line, and a partially-read ribbon at 67%.
- **Moonlit Cartographer** (web novel, completed run) —
  `total_chapters=140`, `total_is_known=true`, current chapter 88,
  rating 7, `pub_status=completed`, `year=2021`. Shows the `140` UI
  treatment (no `+`) and a 62% ribbon.

The seeds use real timestamps (2 days ago, 5 days ago) so the relative-
time display ("2d ago") and the "recently active" sort work out of
the box. They can be deleted from the UI on day one without leaving
orphan rows — the `ON DELETE CASCADE` on the entry table handles
cleanup.
