# Development

How to build, run, test, and extend this project. Aimed at someone
picking up the codebase after the original build.

## Prerequisites

### Go toolchain

The `go.mod` declares `go 1.22` for the module. The only external
dependency, `modernc.org/sqlite`, currently requires Go 1.25 or
newer. With Go 1.21+ toolchain management (`GOTOOLCHAIN=auto`, the
default), running `go build` on a Go 1.22 install will auto-download
a newer Go toolchain to satisfy the requirement. This is normal and
happens transparently on first build.

If you're behind a firewall without `proxy.golang.org` access, set
`GOPROXY=off` and vendor the deps (`go mod vendor`) before building
offline.

### SQLite

Not a runtime dependency — `modernc.org/sqlite` is pure Go and needs
no system SQLite library. The DB file is created on first run in the
process's working directory (or wherever `-db` points).

## Build

From the project root:

```sh
CGO_ENABLED=0 go build -o fic-tally .
```

`CGO_ENABLED=0` is critical: it produces a true static binary with no
dynamic library dependencies. Without it, the Go compiler may emit a
dynamically-linked binary even though `modernc.org/sqlite` doesn't
need CGO (other stdlib packages probe for it). Verify with:

```sh
file fic-tally
# expect: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked
```

Strip the binary for a smaller footprint (optional):

```sh
go build -ldflags="-s -w" -o fic-tally .
```

This drops debug info, taking the binary from ~18 MB to ~10 MB. Don't
do this if you want stack traces to include function names in
panics — the binary will still work, but stack traces show bare
addresses.

## Run

```sh
./fic-tally                                   # defaults: 127.0.0.1:4242, ./fic-tally.db
./fic-tally -addr 127.0.0.1:7531              # different port
./fic-tally -db /var/lib/fic-tally/db.sqlite   # persistent DB path
./fic-tally -addr 0.0.0.0:4242                # LAN-accessible (no auth)
```

Open `http://127.0.0.1:4242/` in a browser. The library is seeded
with two example series on first run; delete them from the UI when
you're ready.

The DB file (`fic-tally.db`) plus its WAL/SHM siblings
(`fic-tally.db-wal`, `fic-tally.db-shm`) appear in the working
directory. All three are part of the database — back them up together.

## Test

A smoke test lives at `scripts/smoke_test.sh` (a copy ships inside the
project at `scripts/smoke_test.sh`; the master lives under
`/home/z/my-project/scripts/`). It exercises every HTTP route once
across 44 labeled groups: library render, filters + sorts, add/edit,
entry edit, progress (+1, Set), cover upload, cover **by URL** (incl.
`javascript:` rejection), cover delete, duplicate warning + confirm,
series grouping + self-parent rejection, series delete, stats (counts,
avg rating), streak + completed-this-month lifecycle, batch API (dup
skip, alias note, dry-run, malformed JSON, bare array, per-item
validation), CSV import (dup skip, bad header, dry-run), JSON import via
file upload, JSON/CSV export + round-trip, import page, theme, 404s,
static assets, the Tsundoku→Fic Tally rename check, **alternative
titles + publication status + released year** (add/search/edit/validation),
**duplicate detection via alternative titles** (form + API), **new
fields through import/export/batch API**, the **publication-status
stats breakdown**, **browser-cache hardening** (no-cache + `?v=`),
**server-side UI settings** (round-trip, validation, cross-origin
block), **saved default sort**, **multi-tag + `#tag` search**, the
**app icon**, the **PWA manifest**, **shelves** (save/replace/delete/
validation/active-match), **per-series reading history** (logging,
no-op suppression, week counter, pace hidden without 2+ days), **bulk
status changes** (apply, PRG back, validation), the **full backup
zip** (contents incl. covers + snapshot integrity), the **progress
%, tag autocomplete, cover drop zone + timestamp tooltip** wiring, the
**options page + new publication-status vocabulary** (labels vs.
values, Upcoming removed), **options management** (rename, custom add,
reorder, in-use/protected removal guards, dup/empty-label validation,
settings-API lockout, retired-value import rejection), and the
**bulk-mode toggle + one-time `upcoming` migration** (staged from the
/backup zip, verified via direct DB inspection).

```sh
cd /path/to/fic_tally
bash scripts/smoke_test.sh ./fic-tally
```

It spawns its own server against a clean `fic-tally.db` and exits
non-zero on any unexpected status code. Read the script before
modifying — each section is short and labeled.

For Go-side unit tests: there are none today. The `Store` interface
is the natural seam for them — write table-driven tests against a
temporary `modernc.org/sqlite` DB, exercising Get / List / Save /
SaveAll / ReadDays / Delete. `computeStats` (stats.go), `resolveImport`
(transfer.go), and `findDuplicates` (dedup.go) are pure functions and
even easier to table-test. The handler layer is harder to unit-test
without spinning up a `httptest.Server`; the smoke test covers that
path integration-style.

## Vet

```sh
go vet ./...
```

Should always be clean. Run it before committing.

## Common changes

### Add a new HTTP route

1. Add the handler in `handlers.go` as a method on `*app`:
   ```go
   func (a *app) handleNewThing(w http.ResponseWriter, r *http.Request) {
       // ... read input, mutate store, redirect ...
   }
   ```
2. Register it in `newServer` in `app.go`:
   ```go
   mux.HandleFunc("GET /new-thing/{id}", a.handleNewThing)
   ```
3. Add a template (if it renders HTML) or redirect (if it's a
   mutation).

### Add a new template

Drop a new `.html` file in `templates/`. It will be picked up by
`ParseGlob`. Use the partials pattern — start with
`{{template "header" .}}`, end with `{{template "footer" .}}`, define
the whole thing as `{{define "your-page.html"}}...{{end}}`.

Render it from a handler (settings injection — `SettingsJSON`, `Theme`,
the preference `data-*` attributes — happens automatically inside
`a.render`, so don't pass `Theme` yourself):
```go
a.render(w, r, "your-page.html", map[string]any{
    "Title": "Your Page",
    // ... page-specific data ...
})
```

### Add a new status value

1. Add a constant in `models.go`:
   ```go
   StatusReReading Status = "re-reading"
   ```
2. Add it to `AllStatuses()` so it appears in `<select>` dropdowns.
3. Add a case to `Status.CSSVar()` returning the CSS token name.
4. Add a CSS class rule to `static/css/app.css`:
   ```css
   .dot-re-reading { background: var(--status-re-reading); }
   ```
5. Add a `--status-re-reading: #...` token to the dark and light
   `:root` palettes in the same file.
6. Add a case to the `statusDotClass` template func in `app.go`.

The smoke test will need a new case if you want to cover the new
status in the round-trip test.

### Add a new series type

1. Add a constant in `models.go`:
   ```go
   TypeWebtoon SeriesType = "webtoon"
   ```
2. Add it to `AllSeriesTypes()`.

That's it. Types don't carry color tokens or other UI semantics.

### Change the port

The default is `127.0.0.1:4242`. Override at runtime with
`-addr 127.0.0.1:<port>`, or change the default in `app.go`:

```go
addr = flag.String("addr", "127.0.0.1:4242", "listen address (host:port)")
```

### Change the cover upload size cap

In `handlers.go`, `handleCoverUpload`, the hard cap is enforced by
`http.MaxBytesReader` *before* multipart parsing:

```go
r.Body = http.MaxBytesReader(w, r.Body, 8<<20)   // 8 MiB hard cap
if err := r.ParseMultipartForm(2 << 20); err != nil {
    // MaxBytesError → 413 with "cover image exceeds 8 MiB…"
    // other parse error → 400 with "upload malformed: …"
}
```

The `2 << 20` passed to `ParseMultipartForm` is the in-memory threshold
(forms smaller than this stay in RAM; larger ones spill to a temp file).
It is **not** the upload limit. The `8<<20` on `MaxBytesReader` is the
actual ceiling; bump that constant to raise the cap.

## Gotchas

### UI preferences are SERVER-side now (settings table)

Layout / ribbon / emblem / theme prefs used to live in per-browser
localStorage (+ a theme cookie). They now live in the SQLite `settings`
 table and are injected into every page by `a.render` (see
`settings.go` and the architecture doc). Consequences:

- Handlers must NOT pass `"Theme"` in the template data map — `render`
  owns it (the old `themeFromCookie` helper is gone).
- `a.render` and `a.serverError` take the request: `a.render(w, r, name, data)`.
- New client-side knobs should go through `persistPrefs()` in `app.js`
  (debounced `POST /api/settings`) and get a validated entry in
  `parseSettingsPatch` — the server only ever stores enums, `#rrggbb`
  colors and bounded numbers, because the stored blobs are re-rendered
  into pages (`#ft-settings` blob is `template.JS`, i.e. verbatim).
- Changing `app.js` or `app.css` requires bumping `?v=` on BOTH asset
  URLs in `templates/layout.html` (see the stale-cache gotcha below).
- The settings table is per-database: copying/moving `fic-tally.db`
  moves the prefs with it. That's the feature, not a bug.

### Browser cache serves stale CSS/JS after an upgrade

Go's `http.FileServer` sends only a `Last-Modified` header — no
`Cache-Control`. Without any freshness directive browsers fall back to
**heuristic caching** (roughly: "reuse without asking if the cached copy
is younger than ~10% of its age") and can keep serving an old
`app.js`/`app.css` for days. Symptom seen in the wild: the user upgraded
the binary and templates, reloaded, and got **new HTML + old assets** —
cards rendered as a raw text dump (the new `card-extra` classes had no
rules in the old stylesheet), the layout buttons did nothing, and search
still fired on every keystroke (the old script's debounce). It looks
exactly like the upgrade failed.

Two defenses are in place:

1. `noCache` middleware (`app.go`) sets `Cache-Control: no-cache` on
   every response. The browser must revalidate before reusing a cached
   copy; unchanged files answer `304`, so it stays cheap.
2. Asset URLs in `templates/layout.html` carry a `?v=` cache-buster.
   A changed asset gets a fresh URL that cannot match any cached entry —
   this rescues browsers that already hold a stale pre-`no-cache` copy,
   without requiring a hard refresh.

When you change `static/css/app.css` or `static/js/app.js`, bump BOTH
`v=` values in `templates/layout.html` (defense #2 only works if the
version moves). Also note templates are parsed once at **startup** —
template edits on disk require restarting the server before they show
up.

### `grep -q` + `pipefail` = flaky test assertions (SIGPIPE)

The smoke suite runs under `set -o pipefail`. In a pipeline like
`curl -s … | grep -q "needle"`, `grep -q` exits at the FIRST match —
if curl is still writing the response body, it receives SIGPIPE and
dies with status 141, and pipefail turns that into a failure for the
whole pipeline. The assertion then reports "body missing X" even
though the match demonstrably succeeded — and only ~1 run in 2-5,
because it depends on TCP segmentation timing.

Fix (applied throughout `smoke_test.sh`): capture first, then grep a
herestring — bash writes herestrings to a fully-buffered temp file, so
there is no pipe and no race:

```bash
BODY=$(curl -s "http://…/page")     # curl writes into a var: safe
grep -q "needle" <<< "$BODY"        # herestring: no pipe, no SIGPIPE
```

Do not reintroduce `… | grep -q` in that script (see the warning in
its header). Status-code probes use `[ "$(curl … -w '%{http_code}')" = "200" ]` instead.

### `html/template` rejects inline `var()` interpolation

If you write:
```html
<span style="background:var({{someFunc}})">
```
where `someFunc` returns a string like `--status-reading`, the
template engine will emit `var(ZgotmplZ)` instead. This is the
context-aware escaper refusing to inject untrusted CSS into a
style attribute.

Fix: use a CSS class instead. The `statusDotClass` template func
returns `dot-reading` (a class suffix); the CSS file defines
`.dot-reading { background: var(--status-reading); }`. This is
cleaner anyway.

### `modernc.org/sqlite` driver name is `"sqlite"`, not `"sqlite3"`

If you swap in `mattn/go-sqlite3`, change both the import
(`github.com/mattn/go-sqlite3`) and the driver name in
`sql.Open("sqlite3", ...)`. Otherwise the driver won't register and
you'll get a confusing "sql: unknown driver" error.

### `time.Time` zero value is not "never" in SQL

`last_read_at` is `TEXT NOT NULL` in the schema, so it can't be NULL
in the database. The Go zero value (`time.Time{}`) serializes as
`"0001-01-01T00:00:00Z"`. The `relTime()` helper checks for
`.IsZero()` and returns `"—"` for display, but the database stores a
real timestamp even for "never read."

For new Entry rows (status=`plan to read`, no chapter yet), the seed
and `Save` code sets `last_read_at` to the creation time. This is
acceptable: an unread entry's "recently active" sort key falls back
to "when it was added," which is sensible.

### `Store.Save` is the only place that mutates state

There is no `UpdateEntry`, `UpdateProgress`, `UpdateCover`, etc.
The handler layer reconstructs the full Series + Entry from form
input (plus a `Get` for fields not on the form, like
`last_read_at`), then calls `Save(ser, ent, advanceProgress)` which
upserts both rows in one transaction.

This is deliberate: a single mutation entry point means the
`updated_at` / `last_read_at` bumping logic lives in exactly one
place. If you add a new handler that mutates state, route it through
`Store.Save`.

### Template `{{define "name"}}` collision across files

If you add a new template file that defines a template name also
defined in another file (like `{{define "content"}}`), the last
file parsed by `ParseGlob` wins, and the other file's definition is
silently dropped. Use unique names — typically the file's basename,
e.g., `{{define "library.html"}}`. See `ARCHITECTURE.md` for why.

## Debugging

### Server logs

Logs go to stderr. Capture with `./fic-tally 2>&1 | tee server.log`
for inspection. Errors include `[error] <where>: <err>` for store
failures and `listening on http://...` at startup.

### SQLite inspection

```sh
sqlite3 fic-tally.db
> .tables
> .schema series
> SELECT id, title, total_chapters, total_is_known FROM series;
> SELECT series_id, status, current_chapter_label, last_read_at FROM entry;
```

If `sqlite3` is not installed, the `modernc.org/sqlite` Go driver
has no CLI; use a small Go script to query the DB, or install
`sqlite3` via your package manager.

### Template not rendering

1. Check that the template file is in the `templates/` directory
   and named `<name>.html`.
2. Check that the file starts with `{{define "<name>.html"}}` and
   ends with `{{end}}`.
3. Call `a.render(w, "<name>.html", data)` — the name must match the
   `define` block, not the filename (they usually match, but the
   `define` block is what's executed).
4. If the response is a 500 with "template clone failed" or a
   "template doesn't exist" error, the templates didn't parse. Run
   `go run . -templates templates/` and check stderr.

## Files that should not be committed

- `fic-tally` (the binary) — rebuild from source.
- `fic-tally.db`, `fic-tally.db-wal`, `fic-tally.db-shm` — runtime data.
- `static/covers/*` — user-uploaded images.
- `server.log` — runtime logs.

A reasonable `.gitignore`:
```
fic-tally
fic-tally.db*
static/covers/
server.log
*.log
```

## Database migrations (existing installs)

Migrations are additive and run automatically at startup in
`migrate()` (sqlite_store.go):

- `series.parent_id TEXT NOT NULL DEFAULT ''` — series grouping.
- `series.alt_titles TEXT NOT NULL DEFAULT '[]'` — alternative titles.
- `series.year INTEGER NOT NULL DEFAULT 0` — released year (0 = unknown).
- `series.pub_status TEXT NOT NULL DEFAULT ''` — publication status.
- `entry.completed_at TEXT NOT NULL DEFAULT ''` — completed-this-month.
- `daily_reads` table — streak + activity counters.

Each is guarded by a `PRAGMA table_info` check, so it runs exactly once
per database and never touches data that already matches. There is no
down-migration; if you need to roll back, restore a backup (use
`GET /export/json` — it captures the full library, minus cover files
which live under `static/covers/`).

Old `tsundoku.db` files are detected at startup: when no `fic-tally.db`
exists but `tsundoku.db` does, the old file is used as-is (with
migrations applied in place). Rename or delete it to move to the new
default filename.

## Scripting the batch API

Bulk-adding from Python/Node/shell should use `POST /api/series/batch`
rather than one form POST per series: one request + one transaction for
up to 1000 rows, with per-row verdicts in the JSON response.

```sh
# from a file (e.g. an /export/json backup)
curl -X POST http://127.0.0.1:4242/api/series/batch \
     -H 'Content-Type: application/json' \
     --data @export.json

# validate first, write later
curl -X POST http://127.0.0.1:4242/api/series/batch \
     -H 'Content-Type: application/json' \
     -d '{"series":[{"title":"Probe"}],"dry_run":true}'
```

For CSV sources, either convert to the JSON shape or use the import
page's endpoint directly (`POST /import` with a `payload` field).
