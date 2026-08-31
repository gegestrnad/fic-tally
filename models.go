package main

// models.go — data types matching the spec (rev1) data model, extended with
// the user-requested additions: Series.ParentID (series grouping),
// Entry.CompletedAt (drives "completed this month" on the stats page),
// Series.AltTitles (translated/alternative titles, searchable),
// Series.PubStatus (publication status: ongoing/completed/hiatus/...) and
// Series.Year (first release year).
//
// Series and Entry are kept structurally separate. A Series is bibliographic
// metadata that may be refreshed later without touching personal tracking
// data. An Entry is the user's personal tracking data for a Series.
//
// The two are joined at read time by series_id; the Store layer surfaces them
// together via EntryWithSeries for the UI, but persistence keeps them on
// separate tables so a metadata refresh can blow away and rewrite the
// Series row without losing the Entry.

import "time"

// SeriesType is the work-type of a series. Stored as TEXT; the canonical
// values of the built-in options are lowercase ("manga", "light novel").
type SeriesType string

// SeriesType enumerates the BUILT-IN work types. These constants are the
// canonical values of the default option list (see options.go) — the list
// itself is user-editable, so validation and dropdown rendering read the
// live option lists, not this slice. The constants stay because the
// import default (type=manga) and the seeded examples reference them.
const (
        TypeManga      SeriesType = "manga"
        TypeManhwa     SeriesType = "manhwa"
        TypeManhua     SeriesType = "manhua"
        TypeLightNovel SeriesType = "light novel"
        TypeWebNovel   SeriesType = "web novel"
)

// Status is the user's relationship to a series. Stored as TEXT in the
// entries table; the value (not the label) is what URLs, filters, imports
// and the database carry.
type Status string

// Status enumerates the BUILT-IN reading statuses. As with SeriesType, the
// dropdown contents live in the user-editable options (options.go); these
// constants are the semantic anchors: "completed" drives the completed_at
// transition and the completion emblem, "reading" the Currently-Reading
// stats tile, "plan to read" the new-series/import default. They are
// protected from removal (labels can still be renamed) — see options.go.
// Full string values per spec ("plan to read", "on hold", "completed") —
// not the abbreviated forms the mockup JS used internally.
const (
        StatusReading    Status = "reading"
        StatusPlanToRead Status = "plan to read"
        StatusOnHold     Status = "on hold"
        StatusDropped    Status = "dropped"
        StatusCompleted  Status = "completed"
)

// PubStatus is the PUBLICATION status of the work itself — whether the
// series is still being written/published. This is deliberately separate
// from Status above, which tracks the USER's relationship to the series
// (reading / completed / ...). A series you finished reading can still be
// an ongoing publication, and vice versa.
// Stored as TEXT; empty string means "unknown / not set".
//
// The five historical values were ongoing/completed/hiatus/cancelled/
// upcoming. v8 made the list user-editable with renamed labels
// (Ongoing/Complete/Hiatus/Canceled) and dropped "upcoming"; the stored
// VALUES are unchanged so no data migration is needed — except the removed
// "upcoming", whose rows are cleared to "" once at startup (initOptions).
type PubStatus string

const (
        PubOngoing   PubStatus = "ongoing"
        PubCompleted PubStatus = "completed"
        PubHiatus    PubStatus = "hiatus"
        PubCancelled PubStatus = "cancelled"
)

// Series is bibliographic metadata for a work. This row can be deleted and
// rewritten by a future metadata refresh without affecting the Entry row.
//
// ParentID links a series to its "parent" (a main series when this row is a
// spinoff/prequel/sequel in the same universe). Empty string = standalone.
// It is deliberately a soft reference (no DB-level FK) so deleting a parent
// only detaches children (the store clears parent_id on delete) instead of
// cascading or failing.
type Series struct {
        ID             string     `json:"id"`
        Title          string     `json:"title"`
        AltTitles      []string   `json:"alt_titles,omitempty"`      // alternative/translated titles; searchable
        Type           SeriesType `json:"type"`
        Author         string     `json:"author"`
        Year           int        `json:"year,omitempty"`            // first release year; 0 = unknown
        PubStatus      PubStatus  `json:"pub_status,omitempty"`      // publication status; "" = unknown
        Description    string     `json:"description"`
        CoverURL       string     `json:"cover_url"`               // remote URL or /static/covers/<id>.<ext>
        Tags           []string   `json:"tags"`
        SourceURL      string     `json:"source_url"`
        ParentID       string     `json:"parent_id"`               // series grouping; "" = standalone
        TotalChapters  *float64   `json:"total_chapters"`        // nullable; nil = unknown
        TotalIsKnown   bool       `json:"total_is_known"`        // false → UI shows "210+"
        CreatedAt      time.Time  `json:"created_at"`
}

// Entry is the user's personal tracking data for a Series. One Entry per
// Series (1:1); series_id is the primary key.
type Entry struct {
        SeriesID            string    `json:"series_id"`
        Status              Status    `json:"status"`
        CurrentChapterNum   *float64  `json:"current_chapter_number"` // nullable; nil when no numeric position (e.g. "Extra 1")
        CurrentChapterLabel string   `json:"current_chapter_label"`  // always populated; what's actually shown
        Rating              *int      `json:"rating"`                 // nullable, 1-10
        Notes               string    `json:"notes"`                   // free-form review / thoughts
        BookmarkURL         string    `json:"bookmark_url"`
        BookmarkLabel       string    `json:"bookmark_label"`        // e.g. "Chapter 143"
        UpdatedAt           time.Time `json:"updated_at"`            // bumps on ANY edit
        LastReadAt          time.Time `json:"last_read_at"`          // bumps ONLY when chapter advances
        CompletedAt         time.Time `json:"completed_at"`          // set when status transitions TO completed; zero otherwise
}

// EntryWithSeries is the joined view the UI actually consumes.
type EntryWithSeries struct {
        Series
        Entry
}

// ChapterLog is one row of a series' reading history — written every time
// a progress update actually changes the numeric chapter (via +1, Set, or
// Clear num on the detail page). It powers the per-series history list,
// the "chapters this week" figure, and the finish-date estimate on the
// detail page. Unlike daily_reads (a global per-day counter), this table
// keeps the per-series detail: which chapter, when, and by how much it
// moved.
type ChapterLog struct {
        SeriesID string
        Chapter  *float64 // numeric position AFTER this update; nil = cleared
        Label    string   // display label AFTER this update ("142", "Extra 1")
        Delta    float64  // signed change vs the previous position (0 when unknown)
        At       time.Time
}
