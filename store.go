package main

// store.go — persistence layer.
//
// The Store interface is the only persistence contract the rest of the app
// depends on. The spec calls for it explicitly: "put persistence behind a
// small interface (Get, List, Save, Delete) from day one, even while the
// only implementation is a JSON file. Moving to SQLite later ... becomes a
// second implementation of that interface instead of a rewrite."
//
// We're starting with the SQLite implementation directly (per user choice)
// but the interface stays narrow so a JSON-file or Postgres implementation
// can be added without touching handlers or templates.

// Store is the persistence contract. All methods return joined
// EntryWithSeries rows because that's the only shape the UI consumes;
// callers don't need to know about the Series/Entry split at read time.
type Store interface {
        // Get returns one series+entry by series ID. Returns ErrNotFound if
        // either the Series row or the Entry row is missing.
        Get(id string) (*EntryWithSeries, error)

        // List returns all series+entry rows, unfiltered. The handler layer
        // is responsible for filtering/sorting in memory — the library is
        // small (single-user) and this keeps List simple and predictable.
        List() ([]EntryWithSeries, error)

        // Save upserts the Series and Entry together. Both are written in a
        // single transaction; if either write fails, neither is committed.
        // Entry.UpdatedAt is bumped here. Entry.LastReadAt is bumped here only
        // if advanceProgress is true (the caller signals "this edit actually
        // moved the chapter forward" vs "this edit just touched metadata").
        Save(s Series, e Entry, advanceProgress bool) error

        // SaveAll upserts multiple rows in ONE transaction. This is the batch
        // path used by import and the JSON API: N rows cost one BEGIN/COMMIT
        // round-trip instead of N, which is what makes bulk input efficient.
        // Per-row semantics are identical to Save.
        SaveAll(items []SaveItem) error

        // ReadDays returns a map of UTC date ("2006-01-02") → number of
        // chapter-advances logged that day. Backs the reading streak and the
        // activity strip on the stats page.
        ReadDays() (map[string]int, error)

        // Delete removes both the Series and Entry rows for the given ID.
        // Idempotent: deleting an unknown ID is a no-op, not an error.
        // Any series whose ParentID pointed at the deleted ID is detached
        // (parent_id cleared) rather than deleted.
        Delete(id string) error

        // Settings returns every stored UI-preference blob as a map of
        // group key ("layout" / "ribbon" / "emblem" / "theme") → canonical
        // JSON value. Preferences are server-side (they follow the database)
        // rather than per-browser; see settings.go.
        Settings() (map[string]string, error)

        // SaveSettings upserts preference blobs in one transaction. Values are
        // expected to be pre-validated canonical JSON (parseSettingsPatch).
        SaveSettings(kv map[string]string) error

        // AppendLog records one chapter update in the series' reading
        // history (date, chapter, signed delta). Called by the progress
        // handler whenever the numeric chapter actually changes; powers the
        // per-series history, "chapters this week" and the finish estimate.
        AppendLog(seriesID string, chapter *float64, label string, delta float64) error

        // ChapterLog returns the reading history for a series, newest first
        // (capped at 100 entries — the detail page shows fewer).
        ChapterLog(seriesID string) ([]ChapterLog, error)

        // Snapshot writes a consistent, self-contained copy of the whole
        // database to dst (used by GET /backup). For SQLite this is
        // VACUUM INTO, which takes the same locks as a read transaction —
        // concurrent requests keep working.
        Snapshot(dst string) error

        // StatusUsage / TypeUsage / PubStatusUsage return value → number of
        // series currently using that option. They back the "N in use"
        // hints on the options page and the removal guards in
        // handleOptionsSave (an option still referenced by a row can't be
        // deleted, so no series can end up holding an orphaned value).
        StatusUsage() (map[string]int, error)
        TypeUsage() (map[string]int, error)
        PubStatusUsage() (map[string]int, error)

        // ClearPubStatusValue sets pub_status to '' (unknown) for every
        // series holding the given value. One-time migration helper used
        // when the options vocabulary drops a publication status (v8:
        // "upcoming").
        ClearPubStatusValue(value string) error
}

// SaveItem is one row-tuple for SaveAll.
type SaveItem struct {
        Series   Series
        Entry    Entry
        Advance  bool // true → bump last_read_at and log a daily-read event
}
