package main

// sqlite_store.go — SQLite-backed implementation of Store.
//
// Uses modernc.org/sqlite (pure-Go, no CGO) so the build produces a true
// static binary (spec goal: "one static binary, zero runtime"). Drop-in
// for mattn/go-sqlite3 if CGO is acceptable — swap the driver name and
// import line; nothing else changes.
//
// Schema mirrors the data model exactly. Tags stored as JSON-encoded TEXT
// because SQLite has no native array type. Nullable numerics use sql.Null*
// types on the way in/out to distinguish "0" from "unknown".

import (
        "database/sql"
        "encoding/json"
        "errors"
        "fmt"
        "strings"
        "time"

        _ "modernc.org/sqlite"
)

// ErrNotFound is returned by Store.Get when no row matches the requested ID.
// Handlers translate this into a 404.
var ErrNotFound = errors.New("not found")

// SQLiteStore implements Store on top of a single *sql.DB.
type SQLiteStore struct {
        db *sql.DB
}

// NewSQLiteStore opens (or creates) the database at path, runs migrations,
// and seeds the two example series if the library is empty.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
        // modernc.org/sqlite driver registers as "sqlite" (not "sqlite3").
        // _pragma=json("{...}") lets us configure busy_timeout and foreign_keys
        // at open time without a separate PRAGMA round-trip.
        dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
        db, err := sql.Open("sqlite", dsn)
        if err != nil {
                return nil, fmt.Errorf("open sqlite: %w", err)
        }
        // Single-writer, single-connection. SQLite handles concurrency by
        // serializing writes through one connection in WAL mode; we set WAL
        // below. SetMaxOpenConns(1) avoids "database is locked" errors when
        // the http handler pool fires concurrent reads during a write.
        db.SetMaxOpenConns(1)

        s := &SQLiteStore{db: db}
        if err := s.migrate(); err != nil {
                return nil, fmt.Errorf("migrate: %w", err)
        }
        if err := s.seedIfEmpty(); err != nil {
                return nil, fmt.Errorf("seed: %w", err)
        }
        return s, nil
}

func (s *SQLiteStore) migrate() error {
        // PRAGMA journal_mode=WAL gives readers non-blocking access to the
        // DB while a write is in flight. Essential for a server process.
        if _, err := s.db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
                return fmt.Errorf("set WAL: %w", err)
        }
        _, err := s.db.Exec(`
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

CREATE TABLE IF NOT EXISTS daily_reads (
  date  TEXT PRIMARY KEY, -- UTC "2006-01-02"
  count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY, -- layout | ribbon | emblem | theme
  value      TEXT NOT NULL,    -- canonical JSON for the group
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chapter_log (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  series_id TEXT NOT NULL,
  chapter   REAL,             -- numeric position AFTER the update; NULL = cleared
  label     TEXT NOT NULL DEFAULT '',
  delta     REAL NOT NULL DEFAULT 0, -- signed change vs previous position
  at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chapter_log_series ON chapter_log(series_id, id);
`)
        if err != nil {
                return fmt.Errorf("create tables: %w", err)
        }
        // Additive column migrations for databases created by older builds.
        // SQLite has no "ADD COLUMN IF NOT EXISTS", so guard with table_info.
        cols := map[string]bool{}
        rows, err := s.db.Query(`PRAGMA table_info(series)`)
        if err != nil {
                return fmt.Errorf("pragma series: %w", err)
        }
        for rows.Next() {
                var cid int
                var name, ctype string
                var notnull int
                var dflt any
                var pk int
                if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
                        rows.Close()
                        return err
                }
                cols[name] = true
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                return err
        }
        if !cols["parent_id"] {
                if _, err := s.db.Exec(`ALTER TABLE series ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`); err != nil {
                        return fmt.Errorf("add series.parent_id: %w", err)
                }
        }
        if !cols["alt_titles"] {
                if _, err := s.db.Exec(`ALTER TABLE series ADD COLUMN alt_titles TEXT NOT NULL DEFAULT '[]'`); err != nil {
                        return fmt.Errorf("add series.alt_titles: %w", err)
                }
        }
        if !cols["year"] {
                if _, err := s.db.Exec(`ALTER TABLE series ADD COLUMN year INTEGER NOT NULL DEFAULT 0`); err != nil {
                        return fmt.Errorf("add series.year: %w", err)
                }
        }
        if !cols["pub_status"] {
                if _, err := s.db.Exec(`ALTER TABLE series ADD COLUMN pub_status TEXT NOT NULL DEFAULT ''`); err != nil {
                        return fmt.Errorf("add series.pub_status: %w", err)
                }
        }

        ecols := map[string]bool{}
        rows, err = s.db.Query(`PRAGMA table_info(entry)`)
        if err != nil {
                return fmt.Errorf("pragma entry: %w", err)
        }
        for rows.Next() {
                var cid int
                var name, ctype string
                var notnull int
                var dflt any
                var pk int
                if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
                        rows.Close()
                        return err
                }
                ecols[name] = true
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                return err
        }
        if !ecols["completed_at"] {
                if _, err := s.db.Exec(`ALTER TABLE entry ADD COLUMN completed_at TEXT NOT NULL DEFAULT ''`); err != nil {
                        return fmt.Errorf("add entry.completed_at: %w", err)
                }
        }
        return nil
}

// Get returns one joined row. Series and Entry are 1:1 by series_id.
func (s *SQLiteStore) Get(id string) (*EntryWithSeries, error) {
        row := s.db.QueryRow(`
SELECT s.id, s.title, s.alt_titles, s.type, s.author, s.year, s.pub_status,
       s.description, s.cover_url, s.tags, s.source_url, s.parent_id,
       s.total_chapters, s.total_is_known, s.created_at,
       e.status, e.current_chapter_num, e.current_chapter_label, e.rating,
       e.notes, e.bookmark_url, e.bookmark_label, e.updated_at, e.last_read_at, e.completed_at
FROM series s
LEFT JOIN entry e ON e.series_id = s.id
WHERE s.id = ?`, id)
        out, err := scanJoined(row)
        if err != nil {
                if errors.Is(err, sql.ErrNoRows) {
                        return nil, ErrNotFound
                }
                return nil, err
        }
        return out, nil
}

// List returns all rows, unordered. Handlers filter/sort in memory because
// the library is single-user and tiny; doing it in Go keeps the SQL simple
// and the filter logic auditable in one place.
func (s *SQLiteStore) List() ([]EntryWithSeries, error) {
        rows, err := s.db.Query(`
SELECT s.id, s.title, s.alt_titles, s.type, s.author, s.year, s.pub_status,
       s.description, s.cover_url, s.tags, s.source_url, s.parent_id,
       s.total_chapters, s.total_is_known, s.created_at,
       e.status, e.current_chapter_num, e.current_chapter_label, e.rating,
       e.notes, e.bookmark_url, e.bookmark_label, e.updated_at, e.last_read_at, e.completed_at
FROM series s
LEFT JOIN entry e ON e.series_id = s.id
ORDER BY s.title COLLATE NOCASE`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()

        var out []EntryWithSeries
        for rows.Next() {
                item, err := scanJoined(rows)
                if err != nil {
                        return nil, err
                }
                out = append(out, *item)
        }
        return out, rows.Err()
}

// scanner is the union of *sql.Row and *sql.Rows — both implement Scan().
type scanner interface {
        Scan(dest ...any) error
}

// scanJoined reads the 25-column SELECT shape shared by Get and List.
func scanJoined(s scanner) (*EntryWithSeries, error) {
        var (
                ser              Series
                ent              Entry
                altTitlesJSON    string
                tagsJSON         string
                year             sql.NullInt64
                pubStatus        sql.NullString
                coverURL         sql.NullString
                sourceURL        sql.NullString
                parentID         sql.NullString
                totalChapters    sql.NullFloat64
                totalIsKnown     sql.NullBool
                createdAt        sql.NullString
                currentChapterN  sql.NullFloat64
                chapterLabel     sql.NullString
                rating           sql.NullInt64
                notes            sql.NullString
                bookmarkURL      sql.NullString
                bookmarkLabel    sql.NullString
                updatedAt        sql.NullString
                lastReadAt       sql.NullString
                completedAt      sql.NullString
        )
        if err := s.Scan(
                &ser.ID, &ser.Title, &altTitlesJSON, &ser.Type, &ser.Author, &year, &pubStatus,
                &ser.Description, &coverURL, &tagsJSON, &sourceURL, &parentID,
                &totalChapters, &totalIsKnown, &createdAt,
                &ent.Status, &currentChapterN, &chapterLabel, &rating,
                &notes, &bookmarkURL, &bookmarkLabel, &updatedAt, &lastReadAt, &completedAt,
        ); err != nil {
                return nil, err
        }
        ser.Year = int(year.Int64)
        ser.PubStatus = PubStatus(pubStatus.String)
        if err := json.Unmarshal([]byte(altTitlesJSON), &ser.AltTitles); err != nil {
                // Defensive: a malformed alt_titles column shouldn't break the page.
                ser.AltTitles = []string{}
        }
        ser.CoverURL = coverURL.String
        ser.SourceURL = sourceURL.String
        ser.ParentID = parentID.String
        if totalChapters.Valid {
                v := totalChapters.Float64
                ser.TotalChapters = &v
        }
        ser.TotalIsKnown = totalIsKnown.Bool
        ser.CreatedAt = parseTime(createdAt.String)
        if err := json.Unmarshal([]byte(tagsJSON), &ser.Tags); err != nil {
                // Defensive: a malformed tags column shouldn't break the page.
                ser.Tags = []string{}
        }

        ent.SeriesID = ser.ID
        if currentChapterN.Valid {
                v := currentChapterN.Float64
                ent.CurrentChapterNum = &v
        }
        ent.CurrentChapterLabel = chapterLabel.String
        if rating.Valid {
                v := int(rating.Int64)
                ent.Rating = &v
        }
        ent.Notes = notes.String
        ent.BookmarkURL = bookmarkURL.String
        ent.BookmarkLabel = bookmarkLabel.String
        ent.UpdatedAt = parseTime(updatedAt.String)
        ent.LastReadAt = parseTime(lastReadAt.String)
        ent.CompletedAt = parseTime(completedAt.String)

        return &EntryWithSeries{Series: ser, Entry: ent}, nil
}

// Save upserts both rows in a single transaction. Entry.UpdatedAt always
// bumps; Entry.LastReadAt only when advanceProgress is true.
func (s *SQLiteStore) Save(ser Series, ent Entry, advanceProgress bool) error {
        return s.SaveAll([]SaveItem{{Series: ser, Entry: ent, Advance: advanceProgress}})
}

// SaveAll upserts every item in ONE transaction — the batch path used by
// import and the JSON API. All-or-nothing: a mid-batch failure rolls the
// whole batch back so a partial import never lands.
func (s *SQLiteStore) SaveAll(items []SaveItem) error {
        tx, err := s.db.Begin()
        if err != nil {
                return err
        }
        defer tx.Rollback()
        for _, it := range items {
                if err := saveTx(tx, it.Series, it.Entry, it.Advance); err != nil {
                        return err
                }
        }
        return tx.Commit()
}

// saveTx upserts one Series+Entry pair inside an existing transaction.
// completed_at lifecycle is centralized here so every write path (form,
// progress, batch API, import) gets the same transition handling:
//   - status → completed (from a non-completed status): set completed_at = now
//   - status → anything else while completed_at was set: clear it
//   - otherwise: preserve the stored value
func saveTx(tx *sql.Tx, ser Series, ent Entry, advanceProgress bool) error {
        tagsJSON, _ := json.Marshal(ser.Tags)
        if ser.Tags == nil {
                tagsJSON = []byte("[]")
        }
        altJSON, _ := json.Marshal(ser.AltTitles)
        if ser.AltTitles == nil {
                altJSON = []byte("[]")
        }

        _, err := tx.Exec(`
INSERT INTO series (id, title, alt_titles, type, author, year, pub_status, description, cover_url, tags, source_url, parent_id, total_chapters, total_is_known, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  title=excluded.title, alt_titles=excluded.alt_titles, type=excluded.type,
  author=excluded.author, year=excluded.year, pub_status=excluded.pub_status,
  description=excluded.description, cover_url=excluded.cover_url,
  tags=excluded.tags, source_url=excluded.source_url, parent_id=excluded.parent_id,
  total_chapters=excluded.total_chapters, total_is_known=excluded.total_is_known`,
                ser.ID, ser.Title, string(altJSON), string(ser.Type), ser.Author, ser.Year,
                string(ser.PubStatus), ser.Description,
                ser.CoverURL, string(tagsJSON), ser.SourceURL, ser.ParentID, nullableFloat(ser.TotalChapters),
                boolToInt(ser.TotalIsKnown), ser.CreatedAt.UTC().Format(time.RFC3339),
        )
        if err != nil {
                return fmt.Errorf("upsert series: %w", err)
        }

        now := time.Now().UTC()
        ent.UpdatedAt = now
        if advanceProgress {
                ent.LastReadAt = now
        }

        // For an INSERT (no existing entry), last_read_at must still have a
        // value to satisfy NOT NULL. Use the entry's existing last_read_at if
        // present, else fall back to now.
        if ent.LastReadAt.IsZero() {
                ent.LastReadAt = now
        }

        // completed_at transition handling (see function comment).
        var oldStatus sql.NullString
        var oldCompleted sql.NullString
        if err := tx.QueryRow(`SELECT status, completed_at FROM entry WHERE series_id = ?`, ser.ID).
                Scan(&oldStatus, &oldCompleted); err != nil && !errors.Is(err, sql.ErrNoRows) {
                return fmt.Errorf("read prior entry: %w", err)
        }
        switch {
        case ent.Status == StatusCompleted && oldStatus.Valid && oldStatus.String != string(StatusCompleted):
                ent.CompletedAt = now
        case ent.Status == StatusCompleted && !oldStatus.Valid:
                // freshly created directly as completed (import/API)
                ent.CompletedAt = now
        case ent.Status == StatusCompleted:
                // stays completed — keep prior completed_at if there is one
                if t := parseTime(oldCompleted.String); !t.IsZero() {
                        ent.CompletedAt = t
                } else {
                        ent.CompletedAt = now
                }
        default:
                ent.CompletedAt = time.Time{}
        }

        _, err = tx.Exec(`
INSERT INTO entry (series_id, status, current_chapter_num, current_chapter_label, rating,
                   notes, bookmark_url, bookmark_label, updated_at, last_read_at, completed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(series_id) DO UPDATE SET
  status=excluded.status,
  current_chapter_num=excluded.current_chapter_num,
  current_chapter_label=excluded.current_chapter_label,
  rating=excluded.rating,
  notes=excluded.notes,
  bookmark_url=excluded.bookmark_url,
  bookmark_label=excluded.bookmark_label,
  updated_at=excluded.updated_at,
  completed_at=excluded.completed_at,
  last_read_at=CASE WHEN ?=1 THEN excluded.last_read_at ELSE entry.last_read_at END`,
                ent.SeriesID, string(ent.Status), nullableFloat(ent.CurrentChapterNum),
                ent.CurrentChapterLabel, nullableInt(ent.Rating),
                ent.Notes, ent.BookmarkURL, ent.BookmarkLabel,
                ent.UpdatedAt.UTC().Format(time.RFC3339),
                ent.LastReadAt.UTC().Format(time.RFC3339),
                completedAtValue(ent.CompletedAt),
                boolToInt(advanceProgress),
        )
        if err != nil {
                return fmt.Errorf("upsert entry: %w", err)
        }

        // Log a daily-read event so streaks and the activity strip are exact.
        // (last_read_at alone can't reconstruct history: reading the same
        // series two days in a row overwrites yesterday's evidence.)
        if advanceProgress {
                today := now.Format("2006-01-02")
                if _, err := tx.Exec(`
INSERT INTO daily_reads (date, count) VALUES (?, 1)
ON CONFLICT(date) DO UPDATE SET count = count + 1`, today); err != nil {
                        return fmt.Errorf("log daily read: %w", err)
                }
        }
        return nil
}

// Delete is idempotent. Missing IDs return nil. Children (series whose
// parent_id pointed here) are detached, not deleted — spinoffs survive their
// parent being removed. The series' reading history is removed with it:
// chapter_log rows are meaningless once the series is gone.
func (s *SQLiteStore) Delete(id string) error {
        tx, err := s.db.Begin()
        if err != nil {
                return err
        }
        defer tx.Rollback()
        if _, err := tx.Exec(`UPDATE series SET parent_id = '' WHERE parent_id = ?`, id); err != nil {
                return fmt.Errorf("detach children: %w", err)
        }
        if _, err := tx.Exec(`DELETE FROM chapter_log WHERE series_id = ?`, id); err != nil {
                return fmt.Errorf("delete chapter log: %w", err)
        }
        if _, err := tx.Exec(`DELETE FROM series WHERE id = ?`, id); err != nil {
                return fmt.Errorf("delete series: %w", err)
        }
        return tx.Commit()
}

// ReadDays returns the daily_reads table as a date → count map.
func (s *SQLiteStore) ReadDays() (map[string]int, error) {
        rows, err := s.db.Query(`SELECT date, count FROM daily_reads`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := map[string]int{}
        for rows.Next() {
                var d string
                var c int
                if err := rows.Scan(&d, &c); err != nil {
                        return nil, err
                }
                out[d] = c
        }
        return out, rows.Err()
}

// Settings returns every stored UI-preference blob (key → canonical JSON).
// Rows are tiny and few (≤4), so one full scan per page render is fine.
func (s *SQLiteStore) Settings() (map[string]string, error) {
        rows, err := s.db.Query(`SELECT key, value FROM settings`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := map[string]string{}
        for rows.Next() {
                var k, v string
                if err := rows.Scan(&k, &v); err != nil {
                        return nil, err
                }
                out[k] = v
        }
        return out, rows.Err()
}

// SaveSettings upserts preference blobs in ONE transaction — the debounced
// client flush may bundle several groups into a single write.
func (s *SQLiteStore) SaveSettings(kv map[string]string) error {
        if len(kv) == 0 {
                return nil
        }
        tx, err := s.db.Begin()
        if err != nil {
                return err
        }
        defer tx.Rollback()
        now := time.Now().UTC().Format(time.RFC3339)
        for k, v := range kv {
                if _, err := tx.Exec(`
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
                        k, v, now); err != nil {
                        return fmt.Errorf("upsert setting %s: %w", k, err)
                }
        }
        return tx.Commit()
}

// usageBy runs a value → count GROUP BY over one column. The empty value
// (unset) is skipped — it is not an option and never blocks anything.
func (s *SQLiteStore) usageBy(query string) (map[string]int, error) {
        rows, err := s.db.Query(query)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := map[string]int{}
        for rows.Next() {
                var v string
                var n int
                if err := rows.Scan(&v, &n); err != nil {
                        return nil, err
                }
                if v != "" {
                        out[v] = n
                }
        }
        return out, rows.Err()
}

// StatusUsage counts entries per reading-status value.
func (s *SQLiteStore) StatusUsage() (map[string]int, error) {
        return s.usageBy(`SELECT status, COUNT(*) FROM entry GROUP BY status`)
}

// TypeUsage counts series per type value.
func (s *SQLiteStore) TypeUsage() (map[string]int, error) {
        return s.usageBy(`SELECT type, COUNT(*) FROM series GROUP BY type`)
}

// PubStatusUsage counts series per publication-status value.
func (s *SQLiteStore) PubStatusUsage() (map[string]int, error) {
        return s.usageBy(`SELECT pub_status, COUNT(*) FROM series GROUP BY pub_status`)
}

// ClearPubStatusValue resets every series holding the given publication
// status to '' (unknown). Migration helper for dropped vocabulary values.
func (s *SQLiteStore) ClearPubStatusValue(value string) error {
        _, err := s.db.Exec(`UPDATE series SET pub_status='' WHERE pub_status=?`, value)
        return err
}

// AppendLog records one chapter update in the series' reading history.
// Written by the progress handler only when the numeric chapter actually
// changed (+1, Set to a different value, or Clear num), so the log reads as
// a clean timeline rather than a stream of no-op saves.
func (s *SQLiteStore) AppendLog(seriesID string, chapter *float64, label string, delta float64) error {
        _, err := s.db.Exec(`INSERT INTO chapter_log (series_id, chapter, label, delta, at) VALUES (?, ?, ?, ?, ?)`,
                seriesID, nullableFloat(chapter), label, delta, time.Now().UTC().Format(time.RFC3339))
        if err != nil {
                return fmt.Errorf("append chapter log: %w", err)
        }
        return nil
}

// ChapterLog returns the reading history for a series, newest first.
// Capped at 100 rows — the detail page shows ~20 and the pace math only
// looks at the last two weeks; older entries stay in the table (they ship
// in backups) but aren't worth loading for every page view.
func (s *SQLiteStore) ChapterLog(seriesID string) ([]ChapterLog, error) {
        rows, err := s.db.Query(`SELECT chapter, label, delta, at FROM chapter_log
WHERE series_id = ? ORDER BY id DESC LIMIT 100`, seriesID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        var out []ChapterLog
        for rows.Next() {
                var l ChapterLog
                var chapter sql.NullFloat64
                var label sql.NullString
                var at string
                if err := rows.Scan(&chapter, &label, &l.Delta, &at); err != nil {
                        return nil, err
                }
                if chapter.Valid {
                        v := chapter.Float64
                        l.Chapter = &v
                }
                l.SeriesID = seriesID
                l.Label = label.String
                l.At = parseTime(at)
                out = append(out, l)
        }
        return out, rows.Err()
}

// Snapshot writes a consistent copy of the database to dst using
// VACUUM INTO. Unlike copying the .db file (which can miss WAL contents or
// catch a half-written page), VACUUM INTO produces a clean, compacted,
// self-contained snapshot under proper locking — concurrent requests keep
// serving while it runs. Requires SQLite ≥ 3.27 (modernc.org/sqlite bundles
// a current one). The destination must not already exist.
func (s *SQLiteStore) Snapshot(dst string) error {
        // VACUUM INTO takes the filename as a SQL string literal, so single
        // quotes in the path must be doubled. os.CreateTemp paths never
        // contain quotes, but the doubling costs nothing and keeps the
        // method safe for any caller.
        escaped := strings.ReplaceAll(dst, "'", "''")
        if _, err := s.db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
                return fmt.Errorf("vacuum into: %w", err)
        }
        return nil
}

// seedIfEmpty populates the library with two example series on first run.
// Lets the user open the app and immediately see what a populated library
// looks like; can be deleted via the UI from day one.
func (s *SQLiteStore) seedIfEmpty() error {
        var count int
        if err := s.db.QueryRow(`SELECT COUNT(*) FROM series`).Scan(&count); err != nil {
                return err
        }
        if count > 0 {
                return nil
        }

        now := time.Now().UTC()
        twoDaysAgo := now.AddDate(0, 0, -2)
        fiveDaysAgo := now.AddDate(0, 0, -5)

        ironCh := float64(142)
        ironTotal := float64(210)
        ironRating := 8
        moonCh := float64(88)
        moonTotal := float64(140)
        moonRating := 7

        seeds := []EntryWithSeries{
                {
                        Series: Series{
                                ID:            "iron-tide",
                                Title:         "Iron Tide",
                                AltTitles:     []string{"Tide of Iron", "Iron Tide (Remaster)"},
                                Type:          TypeManhwa,
                                Author:        "J. Wren",
                                Year:          2019,
                                PubStatus:     PubOngoing,
                                Description:   "A drowned admiral wakes up in a world where the tide itself takes sides. He rebuilds a fleet, a court, and a reason not to sink again.",
                                Tags:          []string{"Isekai", "Naval", "Political"},
                                TotalChapters: &ironTotal,
                                TotalIsKnown:  false, // ongoing → "210+" in UI
                                CreatedAt:     fiveDaysAgo,
                        },
                        Entry: Entry{
                                SeriesID:            "iron-tide",
                                Status:              StatusReading,
                                CurrentChapterNum:   &ironCh,
                                CurrentChapterLabel: "142",
                                Rating:              &ironRating,
                                Notes:               "The naval arc is the highlight. Pacing dipped around ch. 120, picks back up at 135.",
                                BookmarkURL:         "",
                                BookmarkLabel:       "Chapter 143",
                                UpdatedAt:           twoDaysAgo,
                                LastReadAt:          twoDaysAgo,
                        },
                },
                {
                        Series: Series{
                                ID:            "moonlit-cartographer",
                                Title:         "Moonlit Cartographer",
                                Type:          TypeWebNovel,
                                Author:        "R. Solace",
                                Year:          2021,
                                PubStatus:     PubCompleted,
                                Description:   "A cartographer who can only draw maps by moonlight is hired to chart a country that keeps rearranging itself overnight.",
                                Tags:          []string{"Fantasy", "Slow burn", "Mystery"},
                                TotalChapters: &moonTotal,
                                TotalIsKnown:  true, // 140 is the confirmed final chapter
                                CreatedAt:     now.AddDate(0, 0, -10),
                        },
                        Entry: Entry{
                                SeriesID:            "moonlit-cartographer",
                                Status:              StatusReading,
                                CurrentChapterNum:   &moonCh,
                                CurrentChapterLabel: "88",
                                Rating:              &moonRating,
                                Notes:               "Best read in long sittings; the country-reshuffle only pays off over 5+ chapters.",
                                BookmarkURL:         "",
                                BookmarkLabel:       "Chapter 89",
                                UpdatedAt:           fiveDaysAgo,
                                LastReadAt:          fiveDaysAgo,
                        },
                },
        }

        for _, e := range seeds {
                if err := s.Save(e.Series, e.Entry, false); err != nil {
                        return fmt.Errorf("seed %s: %w", e.ID, err)
                }
        }
        return nil
}

// nullableFloat / nullableInt convert Go pointers to sql.Null* for binding.
func nullableFloat(p *float64) any {
        if p == nil {
                return nil
        }
        return *p
}
func nullableInt(p *int) any {
        if p == nil {
                return nil
        }
        return *p
}
func boolToInt(b bool) int {
        if b {
                return 1
        }
        return 0
}

// completedAtValue renders a completed-at timestamp for the entry row;
// zero time → "" (not completed).
func completedAtValue(t time.Time) any {
        if t.IsZero() {
                return ""
        }
        return t.UTC().Format(time.RFC3339)
}

// parseTime accepts an RFC3339 string and returns a zero time for empty input.
// SQLite stores datetimes as TEXT; we don't use its own datetime functions.
func parseTime(s string) time.Time {
        if s == "" {
                return time.Time{}
        }
        t, err := time.Parse(time.RFC3339, s)
        if err != nil {
                return time.Time{}
        }
        return t
}
