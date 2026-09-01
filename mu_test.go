package main

// mu_test.go — unit tests for the MangaUpdates integration: the pure
// mapping functions (muApply and friends), the base36 slug codec, and the
// HTTP client against an httptest server (request shape, caching, retry).
// One handler-level test renders the real templates through the real mux,
// so the lookup panel markup and route wiring are covered end to end.
//
// No test touches the network: the client's baseURL is swapped for an
// httptest server, and the handler test drives the same swap.

import (
        "fmt"
        "io"
        "net/http"
        "net/http/httptest"
        "net/url"
        "strings"
        "testing"
        "time"
)

// --- base36 slug codec -------------------------------------------------------

func TestSlugBase36Roundtrip(t *testing.T) {
        ids := []int64{1, 35, 36, 37, 1234567, 17360452316, 1090693534222}
        for _, id := range ids {
                slug := idToSlug(id)
                back, err := slugBase36ToID(slug)
                if err != nil {
                        t.Fatalf("idToSlug(%d)=%q, decode failed: %v", id, slug, err)
                }
                if back != id {
                        t.Errorf("roundtrip: idToSlug(%d)=%q, slugBase36ToID=%d", id, slug, back)
                }
        }
}

func TestSlugBase36KnownValue(t *testing.T) {
        // Naruto's MU URL is /series/7z3yqqk/naruto → numeric id 17360452316.
        if got := idToSlug(17360452316); got != "7z3yqqk" {
                t.Errorf("idToSlug(17360452316) = %q, want %q", got, "7z3yqqk")
        }
        if got, err := slugBase36ToID("7z3yqqk"); err != nil || got != 17360452316 {
                t.Errorf("slugBase36ToID(7z3yqqk) = %d, %v; want 17360452316", got, err)
        }
        // Uppercase slugs decode the same.
        if got, err := slugBase36ToID("7Z3YQQK"); err != nil || got != 17360452316 {
                t.Errorf("slugBase36ToID(7Z3YQQK) = %d, %v", got, err)
        }
}

func TestSlugBase36Errors(t *testing.T) {
        for _, s := range []string{"", "   ", "not-a-slug!", "z1234567890123456789"} {
                if _, err := slugBase36ToID(s); err == nil {
                        t.Errorf("slugBase36ToID(%q): expected error, got nil", s)
                }
        }
        if idToSlug(0) != "" || idToSlug(-5) != "" {
                t.Errorf("idToSlug(<=0) must be empty")
        }
}

func TestMuIDFromSourceURL(t *testing.T) {
        cases := []struct {
                url  string
                want int64
        }{
                {"https://www.mangaupdates.com/series/7z3yqqk/naruto", 17360452316},
                {"https://www.mangaupdates.com/series/7z3yqqk/naruto/", 17360452316},
                {"https://www.mangaupdates.com/series/7z3yqqk/naruto?edit=true", 17360452316},
                {"https://myanimepage.example/series/7z3yqqk/naruto", 0},      // wrong host, same path shape
                {"https://www.mangaupdates.com/series/n-1/naruto", 0},         // hyphen is not base36
                {"https://www.mangaupdates.com/series/12345/naruto", 1776965}, // all-digit slug decodes as base36 (12345 → 1776965)
                {"https://www.mangaupdates.com/series/7z3yqqk", 0},            // no title segment after slug
                {"https://example.com/anime/naruto", 0},
                {"", 0},
                {"   ", 0},
        }
        for _, c := range cases {
                if got := muIDFromSourceURL(c.url); got != c.want {
                        t.Errorf("muIDFromSourceURL(%q) = %d, want %d", c.url, got, c.want)
                }
        }
}

// --- pure mapping pieces -----------------------------------------------------

func TestMuMapType(t *testing.T) {
        cases := map[string]string{
                "Manga":   "manga",
                "Manhwa":  "manhwa",
                "Manhua":  "manhua",
                "Novel":   "light novel",
                " manga":  "manga", // trimmed
                "Webtoon": "",      // exotic → no built-in
                "Other":   "",
                "":        "",
        }
        for in, want := range cases {
                if got := muMapType(in); got != want {
                        t.Errorf("muMapType(%q) = %q, want %q", in, got, want)
                }
        }
}

func boolPtr(b bool) *bool        { return &b }
func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }

func TestMuPubStatus(t *testing.T) {
        cases := []struct {
                completed *bool
                status    string
                want      PubStatus
        }{
                {boolPtr(true), "", PubCompleted},        // flag wins, empty text
                {boolPtr(true), "Ongoing", PubCompleted}, // flag wins over text
                {boolPtr(false), "Ongoing", PubOngoing},
                {nil, "Ongoing", PubOngoing},
                {nil, "On Hiatus", PubHiatus},
                {nil, "Cancelled", PubCancelled},
                {nil, "Discontinued", PubCancelled},
                {nil, "72 Volumes (Complete)", PubCompleted}, // text fallback for null flag
                {nil, "", ""},                        // nothing to go on
                {nil, "Something else entirely", ""}, // don't guess
                {boolPtr(false), "", ""},
        }
        for i, c := range cases {
                got := muPubStatus(&muSeriesRecord{Completed: c.completed, Status: c.status})
                if got != c.want {
                        t.Errorf("case %d (completed=%v, status=%q) = %q, want %q", i, c.completed, c.status, got, c.want)
                }
        }
}

func TestMuAuthorName(t *testing.T) {
        authors := []muAuthor{
                {Name: "Illustrator Inko", Type: "Inker"},
                {Name: "The Real Author", Type: "Author"},
                {Name: "Second Author", Type: "Author"},
        }
        if got := muAuthorName(authors); got != "The Real Author" {
                t.Errorf("explicit Author type wins: got %q", got)
        }
        noTyped := []muAuthor{{Name: "First", Type: "Artist"}, {Name: "Second", Type: "Artist"}}
        if got := muAuthorName(noTyped); got != "First" {
                t.Errorf("fallback to first author: got %q", got)
        }
        if got := muAuthorName(nil); got != "" {
                t.Errorf("no authors: got %q", got)
        }
}

func TestMuAltTitles(t *testing.T) {
        rec := &muSeriesRecord{
                Title: "Naruto",
                Associated: []muAssocTitle{
                        {Title: "NARUTO"}, // case-insensitive dup of main → dropped
                        {Title: "Naruto: Shippūden"},
                        {Title: "Naruto: Shippūden"},     // exact dup → dropped
                        {Title: "  Naruto: Shippuden  "}, // trimmed, different case → kept
                        {Title: ""},                      // empty → dropped
                },
        }
        want := []string{"Naruto: Shippūden", "Naruto: Shippuden"}
        got := muAltTitles(rec)
        if len(got) != len(want) {
                t.Fatalf("got %v, want %v", got, want)
        }
        for i := range want {
                if got[i] != want[i] {
                        t.Errorf("got %v, want %v", got, want)
                }
        }
}

func TestMuTagUnion(t *testing.T) {
        existing := []string{"Action", "Shounen", "", "  "}
        genres := []muGenre{{Genre: "action"}, {Genre: "Adventure"}, {Genre: "Shounen"}}
        got := muTagUnion(existing, genres)
        want := []string{"Action", "Shounen", "Adventure"}
        if len(got) != len(want) {
                t.Fatalf("got %v, want %v", got, want)
        }
        for i := range want {
                if got[i] != want[i] {
                        t.Errorf("got %v, want %v", got, want)
                }
        }
        // No existing tags: genres pass through, empties dropped.
        got = muTagUnion(nil, []muGenre{{Genre: "Slice of Life"}, {Genre: "  "}})
        if len(got) != 1 || got[0] != "Slice of Life" {
                t.Errorf("got %v", got)
        }
}

// --- muApply ------------------------------------------------------------------

func TestMuApplyCompleted(t *testing.T) {
        rec := &muSeriesRecord{
                SeriesID:      17360452316,
                Title:         "  Naruto  ",
                URL:           "https://www.mangaupdates.com/series/7z3yqqk/naruto",
                Description:   "A ninja's journey.",
                Type:          "Manga",
                Year:          "1999",
                Status:        "72 Volumes (Complete)",
                Completed:     boolPtr(true),
                LatestChapter: intPtr(700),
                Image:         muImage{},
                Genres:        []muGenre{{Genre: "Action"}},
                Authors:       []muAuthor{{Name: "Masashi Kishimoto", Type: "Mangaka"}},
        }
        rec.Image.URL.Original = "https://cdn.mangaupdates.com/covers/2/65/naruto.jpg"

        ser := Series{
                ID:           "existing-id",
                Title:        "Old Title",
                Type:         TypeManhwa,
                ParentID:     "parent-1",
                SourceURL:    "https://example.com/old",
                CoverURL:     "https://example.com/old-cover.jpg",
                Tags:         []string{"Action", "Favorite"},
                TotalIsKnown: false,
        }
        totalBefore := 123.0
        ser.TotalChapters = &totalBefore

        muApply(&ser, rec, defaultOptionLists())

        if ser.Title != "Naruto" {
                t.Errorf("Title = %q, want trimmed %q", ser.Title, "Naruto")
        }
        if ser.Type != TypeManga {
                t.Errorf("Type = %q, want manga", ser.Type)
        }
        if ser.Author != "Masashi Kishimoto" {
                t.Errorf("Author = %q (fallback to first author expected)", ser.Author)
        }
        if ser.Year != 1999 {
                t.Errorf("Year = %d, want 1999", ser.Year)
        }
        if ser.PubStatus != PubCompleted {
                t.Errorf("PubStatus = %q, want completed", ser.PubStatus)
        }
        if ser.Description != "A ninja's journey." {
                t.Errorf("Description = %q", ser.Description)
        }
        if ser.CoverURL != "https://cdn.mangaupdates.com/covers/2/65/naruto.jpg" {
                t.Errorf("CoverURL = %q (remote existing cover should be replaced by MU image)", ser.CoverURL)
        }
        if ser.SourceURL != rec.URL {
                t.Errorf("SourceURL = %q, want MU url", ser.SourceURL)
        }
        // Tags: existing first (Favorite survives), case-insensitive dedupe.
        wantTags := []string{"Action", "Favorite"}
        if len(ser.Tags) != len(wantTags) {
                t.Fatalf("Tags = %v, want %v", ser.Tags, wantTags)
        }
        for i := range wantTags {
                if ser.Tags[i] != wantTags[i] {
                        t.Errorf("Tags = %v, want %v", ser.Tags, wantTags)
                }
        }
        // Completed, volume-only status → no chapter count in the status, so the
        // total is left untouched (latest_chapter is a last-release figure and is
        // never used as the total).
        if ser.TotalChapters == nil || *ser.TotalChapters != 123 {
                t.Errorf("TotalChapters = %v, want untouched 123", ser.TotalChapters)
        }
        if ser.TotalIsKnown {
                t.Errorf("TotalIsKnown = true, want false (volume-only status states no chapter total)")
        }
        // Invariants: identity fields untouched.
        if ser.ID != "existing-id" || ser.ParentID != "parent-1" {
                t.Errorf("ID/ParentID must survive: %q / %q", ser.ID, ser.ParentID)
        }
}

func TestMuApplyOngoingDoesNotSetTotal(t *testing.T) {
        rec := &muSeriesRecord{
                Title:         "One Piece",
                Type:          "Manga",
                Year:          "1997",
                Status:        "Ongoing",
                Completed:     boolPtr(false),
                LatestChapter: intPtr(1100), // CURRENT chapter, not a total
        }
        ser := Series{Title: "Old"}
        muApply(&ser, rec, defaultOptionLists())
        if ser.TotalChapters != nil {
                t.Errorf("ongoing series: TotalChapters = %v, must stay nil", *ser.TotalChapters)
        }
        if ser.TotalIsKnown {
                t.Errorf("ongoing series: TotalIsKnown must stay false")
        }
        if ser.PubStatus != PubOngoing {
                t.Errorf("PubStatus = %q, want ongoing", ser.PubStatus)
        }
}

// TestMuApplyCompletedChapterStatusWins is the The Gamer regression: a
// finished multi-volume work whose latest_chapter (44) is only the end of
// the final volume — the total must come from the status text (511).
func TestMuApplyCompletedChapterStatusWins(t *testing.T) {
        rec := &muSeriesRecord{
                Title:         "The Gamer",
                Type:          "Manhwa",
                Year:          "2013",
                Status:        "511 Chapters (Complete)\n\n S1: 86 Chapters (01-86) + S1 epilogue (86.5)  \n S7: 44 Chapters (468-510)",
                Completed:     boolPtr(true),
                LatestChapter: intPtr(44), // end of volume 7, NOT the total
        }
        ser := Series{Title: "Old"}
        muApply(&ser, rec, defaultOptionLists())
        if ser.TotalChapters == nil || *ser.TotalChapters != 511 {
                t.Errorf("TotalChapters = %v, want 511 (from status text, not latest_chapter 44)", ser.TotalChapters)
        }
        if !ser.TotalIsKnown {
                t.Errorf("TotalIsKnown = false, want true")
        }
}

func TestMuApplyCompletedNoTotalSourceLeavesTotal(t *testing.T) {
        // Finished, but neither a "N Chapters (Complete)" status nor a positive
        // latest_chapter: don't guess — existing total stays untouched.
        rec := &muSeriesRecord{
                Title:         "Mystery",
                Type:          "Manga",
                Status:        "3 Volumes (Complete)",
                Completed:     boolPtr(true),
                LatestChapter: intPtr(0),
        }
        ser := Series{Title: "Old"}
        totalBefore := 99.0
        ser.TotalChapters = &totalBefore
        muApply(&ser, rec, defaultOptionLists())
        if ser.TotalChapters == nil || *ser.TotalChapters != 99 {
                t.Errorf("TotalChapters = %v, want untouched 99", ser.TotalChapters)
        }
        if ser.TotalIsKnown {
                t.Errorf("TotalIsKnown must stay false")
        }
}

func TestMuTotalChapters(t *testing.T) {
        cases := []struct {
                name string
                rec  muSeriesRecord
                want int
        }{
                {
                        name: "season line must not be mistaken for the total (Muscle Joseon)",
                        rec:  muSeriesRecord{Status: "132 Chapters (Hiatus)\nSeason 1: 57 Chapters (Complete)\nSeason 2: 75 Chapters (Complete)", LatestChapter: intPtr(133)},
                        want: 0, // line 1 is a Hiatus (no Complete marker); the 57 season line must not win; no fallback
                },
                {
                        name: "gamer chapter-complete status beats latest_chapter",
                        rec:  muSeriesRecord{Status: "511 Chapters (Complete)\n\n S7: 44 Chapters (468-510)", LatestChapter: intPtr(44)},
                        want: 511,
                },
                {
                        name: "solo leveling with prologue",
                        rec:  muSeriesRecord{Status: "200 Chapters + Prologue (Complete)\n15 Volumes (Complete)", LatestChapter: intPtr(201)},
                        want: 200,
                },
                {
                        name: "novel WN/SS chapters",
                        rec:  muSeriesRecord{Status: "243 WN Chapters + 27 SS Chapters (Complete)"},
                        want: 243,
                },
                {
                        name: "thousands separator",
                        rec:  muSeriesRecord{Status: "1,234 Chapters (Complete)"},
                        want: 1234,
                },
                {
                        name: "volume-only status states no chapter count (Naruto)",
                        rec:  muSeriesRecord{Status: "72 Volumes (Complete)\n24 Combini-ban Volumes (Complete)", LatestChapter: intPtr(700)},
                        want: 0, // latest_chapter is a last-release figure, never the total
                },
                {
                        name: "no complete marker",
                        rec:  muSeriesRecord{Status: "652 Chapters (Ongoing)\n18 Volumes (Ongoing)", LatestChapter: intPtr(235)},
                        want: 0,
                },
                {
                        name: "volume-only, zero latest",
                        rec:  muSeriesRecord{Status: "3 Volumes (Complete)", LatestChapter: intPtr(0)},
                        want: 0,
                },
                {
                        name: "empty status",
                        rec:  muSeriesRecord{Status: ""},
                        want: 0,
                },
        }
        for _, tc := range cases {
                t.Run(tc.name, func(t *testing.T) {
                        if got := muTotalChapters(&tc.rec); got != tc.want {
                                t.Errorf("muTotalChapters = %d, want %d", got, tc.want)
                        }
                })
        }
}

func TestMuApplyLocalCoverWins(t *testing.T) {
        rec := &muSeriesRecord{Title: "X", Type: "Manga"}
        rec.Image.URL.Original = "https://cdn.mangaupdates.com/covers/x.jpg"
        ser := Series{Title: "Old", CoverURL: "/static/covers/abc123.png"}
        muApply(&ser, rec, defaultOptionLists())
        if ser.CoverURL != "/static/covers/abc123.png" {
                t.Errorf("local upload displaced: CoverURL = %q", ser.CoverURL)
        }
}

func TestMuApplyUnknownTypeLeftAlone(t *testing.T) {
        // MU "Webtoon" has no built-in fic-tally type → existing value kept.
        rec := &muSeriesRecord{Title: "X", Type: "Webtoon"}
        ser := Series{Title: "Old", Type: TypeManhua}
        muApply(&ser, rec, defaultOptionLists())
        if ser.Type != TypeManhua {
                t.Errorf("unmapped type must not clobber: Type = %q", ser.Type)
        }
}

func TestMuApplyOptionListGuard(t *testing.T) {
        // A user who removed "light novel" from their type vocabulary: a MU
        // "Novel" record must not write an orphaned value — leave it alone.
        opts := defaultOptionLists()
        opts.Type = []option{{Value: "manga", Label: "Manga"}, {Value: "manhwa", Label: "Manhwa"}}
        rec := &muSeriesRecord{Title: "X", Type: "Novel"}
        ser := Series{Title: "Old", Type: TypeManga}
        muApply(&ser, rec, opts)
        if ser.Type != TypeManga {
                t.Errorf("option guard failed: Type = %q, want existing manga", ser.Type)
        }
        // Same guard for pub status: user removed "cancelled".
        opts2 := defaultOptionLists()
        opts2.PubStatus = []option{{Value: string(PubOngoing), Label: "Ongoing"}}
        rec2 := &muSeriesRecord{Title: "Y", Type: "Manga", Status: "Cancelled"}
        ser2 := Series{Title: "Old", PubStatus: PubOngoing}
        muApply(&ser2, rec2, opts2)
        if ser2.PubStatus != PubOngoing {
                t.Errorf("pub-status guard failed: %q, want existing ongoing", ser2.PubStatus)
        }
}

func TestMuApplyBadYearKept(t *testing.T) {
        rec := &muSeriesRecord{Title: "X", Type: "Manga", Year: "circa 1999"}
        ser := Series{Title: "Old", Year: 2001}
        muApply(&ser, rec, defaultOptionLists())
        if ser.Year != 2001 {
                t.Errorf("unparseable year must not clobber: Year = %d, want 2001", ser.Year)
        }
}

// --- HTTP client (httptest) ----------------------------------------------------

// muNarutoSearchBody is a minimal but shape-accurate /series/search response.
const muNarutoSearchBody = `{
  "total_hits": 1, "page": 1, "perpage": 10,
  "results": [{
    "record": {
      "series_id": 17360452316,
      "title": "Naruto",
      "url": "https://www.mangaupdates.com/series/7z3yqqk/naruto",
      "associated": [{"title": "NARUTO"}],
      "description": "A ninja's journey.",
      "type": "Manga",
      "year": "1999",
      "status": "72 Volumes (Complete)",
      "completed": null,
      "latest_chapter": null,
      "image": {"url": {"original": "https://cdn.mangaupdates.com/covers/2/65/naruto.jpg"}},
      "genres": [{"genre": "Action"}],
      "authors": [{"name": "Masashi Kishimoto", "author_id": 457, "url": "https://www.mangaupdates.com/author/457/masashi-kishimoto", "type": "Mangaka"}]
    },
    "hit_title": "Naruto"
  }]
}`

// muNarutoFetchBody is the full record with completed=true and a final
// chapter — the shape the confirm step consumes.
const muNarutoFetchBody = `{
  "series_id": 17360452316,
  "title": "Naruto",
  "url": "https://www.mangaupdates.com/series/7z3yqqk/naruto",
  "associated": [{"title": "NARUTO"}],
  "description": "A ninja's journey.",
  "type": "Manga",
  "year": "1999",
  "status": "72 Volumes (Complete)",
  "completed": true,
  "latest_chapter": 700,
  "image": {"url": {"original": "https://cdn.mangaupdates.com/covers/2/65/naruto.jpg"}},
  "genres": [{"genre": "Action"}],
  "authors": [{"name": "Masashi Kishimoto", "author_id": 457, "url": "https://www.mangaupdates.com/author/457/masashi-kishimoto", "type": "Mangaka"}]
}`

func TestMUClientSearch(t *testing.T) {
        var gotPath, gotUA, gotAccept string
        var gotBody []byte
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                gotPath = r.URL.Path
                gotUA = r.Header.Get("User-Agent")
                gotAccept = r.Header.Get("Accept")
                if r.Method == http.MethodPost {
                        gotBody, _ = io.ReadAll(r.Body)
                }
                w.Header().Set("Content-Type", "application/json")
                fmt.Fprint(w, muNarutoSearchBody)
        }))
        defer srv.Close()

        c := newMUClient()
        c.baseURL = srv.URL

        hits, err := c.muSearch("naruto", 10)
        if err != nil {
                t.Fatalf("muSearch: %v", err)
        }
        if len(hits) != 1 {
                t.Fatalf("got %d hits, want 1", len(hits))
        }
        h := hits[0]
        if h.SeriesID != 17360452316 || h.Title != "Naruto" || h.Type != "Manga" || h.Year != "1999" {
                t.Errorf("hit = %+v", h)
        }
        if h.CoverURL != "https://cdn.mangaupdates.com/covers/2/65/naruto.jpg" {
                t.Errorf("CoverURL = %q", h.CoverURL)
        }
        if gotPath != "/series/search" {
                t.Errorf("path = %q, want /series/search", gotPath)
        }
        if !strings.Contains(gotUA, "fic-tally") {
                t.Errorf("User-Agent = %q, must credit fic-tally", gotUA)
        }
        if gotAccept != "application/json" {
                t.Errorf("Accept = %q", gotAccept)
        }
        if !strings.Contains(string(gotBody), `"search":"naruto"`) {
                t.Errorf("POST body = %s, want search term", gotBody)
        }
}

func TestMUClientFetchAndCache(t *testing.T) {
        var hits int
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                hits++
                w.Header().Set("Content-Type", "application/json")
                fmt.Fprint(w, muNarutoFetchBody)
        }))
        defer srv.Close()

        c := newMUClient()
        c.baseURL = srv.URL

        rec, err := c.muFetch(17360452316)
        if err != nil {
                t.Fatalf("muFetch: %v", err)
        }
        if rec.Completed == nil || !*rec.Completed {
                t.Errorf("Completed = %v, want true", rec.Completed)
        }
        if rec.LatestChapter == nil || *rec.LatestChapter != 700 {
                t.Errorf("LatestChapter = %v, want 700", rec.LatestChapter)
        }
        if rec.Year != "1999" {
                t.Errorf("Year = %q (must be a string)", rec.Year)
        }
        if hits != 1 {
                t.Fatalf("first fetch hit server %d times", hits)
        }

        // Second fetch: served from cache, server untouched.
        if _, err := c.muFetch(17360452316); err != nil {
                t.Fatalf("cached muFetch: %v", err)
        }
        if hits != 1 {
                t.Errorf("cached fetch re-hit the server: %d hits", hits)
        }
}

func TestMUClientRetryOn429(t *testing.T) {
        var hits int
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                hits++
                if hits == 1 {
                        w.WriteHeader(http.StatusTooManyRequests)
                        return
                }
                w.Header().Set("Content-Type", "application/json")
                fmt.Fprint(w, muNarutoFetchBody)
        }))
        defer srv.Close()

        c := newMUClient()
        c.baseURL = srv.URL
        c.httpClient = &http.Client{Timeout: muHTTPTimeout}
        // Shorten the backoff so the test stays fast.
        old := muRetryBackoff
        muRetryBackoff = time.Millisecond
        defer func() { muRetryBackoff = old }()

        rec, err := c.muFetch(1)
        if err != nil {
                t.Fatalf("expected retry to succeed, got: %v", err)
        }
        if rec.SeriesID != 17360452316 {
                t.Errorf("SeriesID = %d", rec.SeriesID)
        }
        if hits != 2 {
                t.Errorf("hits = %d, want 2 (one 429 + one success)", hits)
        }
}

func TestMUClientHardErrorNoRetry(t *testing.T) {
        var hits int
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                hits++
                w.WriteHeader(http.StatusNotFound)
                fmt.Fprint(w, "no such series")
        }))
        defer srv.Close()

        c := newMUClient()
        c.baseURL = srv.URL
        old := muRetryBackoff
        muRetryBackoff = time.Millisecond
        defer func() { muRetryBackoff = old }()

        if _, err := c.muFetch(999); err == nil {
                t.Fatalf("expected error for 404")
        } else if !strings.Contains(err.Error(), "404") {
                t.Errorf("error should mention the status: %v", err)
        }
        if hits != 1 {
                t.Errorf("404 must not retry: %d hits", hits)
        }
}

// --- handler level: real mux + real templates ----------------------------------

// fakeStore is the minimum Store the lookup handlers need: a set of rows for
// the edit-form routes (Get), List for the parent select, and no-op writes.
type fakeStore struct {
        rows map[string]EntryWithSeries
        seen []string // every method call, for assertions
}

func (f *fakeStore) call(m string) { f.seen = append(f.seen, m) }

func (f *fakeStore) Get(id string) (*EntryWithSeries, error) {
        f.call("Get")
        item, ok := f.rows[id]
        if !ok {
                return nil, ErrNotFound
        }
        out := item
        return &out, nil
}

func (f *fakeStore) List() ([]EntryWithSeries, error) {
        f.call("List")
        out := make([]EntryWithSeries, 0, len(f.rows))
        for _, r := range f.rows {
                out = append(out, r)
        }
        return out, nil
}
func (f *fakeStore) Save(s Series, e Entry, advance bool) error { f.call("Save"); return nil }
func (f *fakeStore) SaveAll(items []SaveItem) error             { f.call("SaveAll"); return nil }
func (f *fakeStore) ReadDays() (map[string]int, error)          { f.call("ReadDays"); return nil, nil }
func (f *fakeStore) Delete(id string) error                     { f.call("Delete"); return nil }
func (f *fakeStore) Settings() (map[string]string, error) {
        f.call("Settings")
        return map[string]string{}, nil
}
func (f *fakeStore) SaveSettings(kv map[string]string) error { f.call("SaveSettings"); return nil }
func (f *fakeStore) AppendLog(seriesID string, chapter *float64, label string, delta float64) error {
        f.call("AppendLog")
        return nil
}
func (f *fakeStore) ChapterLog(seriesID string) ([]ChapterLog, error) {
        f.call("ChapterLog")
        return nil, nil
}
func (f *fakeStore) Snapshot(dst string) error { f.call("Snapshot"); return nil }
func (f *fakeStore) StatusUsage() (map[string]int, error) {
        f.call("StatusUsage")
        return map[string]int{}, nil
}
func (f *fakeStore) TypeUsage() (map[string]int, error) {
        f.call("TypeUsage")
        return map[string]int{}, nil
}
func (f *fakeStore) PubStatusUsage() (map[string]int, error) {
        f.call("PubStatusUsage")
        return map[string]int{}, nil
}
func (f *fakeStore) ClearPubStatusValue(value string) error {
        f.call("ClearPubStatusValue")
        return nil
}

// newTestApp builds a fully wired app: real templates from ./templates, the
// fake store, and an MU client pointed at an httptest server. Returns the
// app, the test server, and the store (so tests can assert no writes).
func newTestApp(t *testing.T, muBody string) (*app, *httptest.Server, *fakeStore) {
        t.Helper()
        muSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                fmt.Fprint(w, muBody)
        }))
        store := &fakeStore{rows: map[string]EntryWithSeries{
                // The series under edit — already linked to the same MU record the
                // fetch returns, so the "current" marker path is exercised too.
                "s1": {
                        Series: Series{
                                ID:        "s1",
                                Title:     "My Series",
                                Type:      TypeManga,
                                ParentID:  "parent-9",
                                SourceURL: "https://www.mangaupdates.com/series/7z3yqqk/naruto",
                                Tags:      []string{"Drama"},
                        },
                        Entry: Entry{
                                SeriesID:            "s1",
                                Status:              StatusReading,
                                CurrentChapterNum:   floatPtr(142),
                                CurrentChapterLabel: "142",
                                Rating:              intPtr(9),
                                Notes:               "user notes",
                        },
                },
                // A parent row so the parent select renders a "parent-9" option.
                "parent-9": {
                        Series: Series{ID: "parent-9", Title: "The Main Series", Type: TypeManga},
                        Entry:  Entry{SeriesID: "parent-9", Status: StatusCompleted},
                },
        }}
        a := &app{store: store, coverDir: t.TempDir(), mu: newMUClient()}
        a.mu.baseURL = muSrv.URL
        a.initOptions()
        tpl, err := loadTemplates(a, "templates")
        if err != nil {
                t.Fatalf("loadTemplates: %v", err)
        }
        a.tpl = tpl
        return a, muSrv, store
}

// TestLookupNewFormSearch renders the ADD form with a search result panel.
func TestLookupNewFormSearch(t *testing.T) {
        a, muSrv, store := newTestApp(t, muNarutoSearchBody)
        defer muSrv.Close()

        srv := httptest.NewServer(newServer(a))
        defer srv.Close()

        body, _ := http.NewRequest(http.MethodPost, srv.URL+"/series/new/lookup",
                strings.NewReader(urlEncodeForm("query", "naruto")))
        body.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        resp, err := http.DefaultClient.Do(body)
        if err != nil {
                t.Fatalf("POST lookup: %v", err)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                t.Fatalf("status = %d, want 200 (a failed lookup must re-render, not 500)", resp.StatusCode)
        }
        html := string(mustReadAll(t, resp.Body))
        if !strings.Contains(html, `id="mu-panel"`) {
                t.Errorf("page missing #mu-panel")
        }
        if !strings.Contains(html, "Naruto") {
                t.Errorf("page missing the hit title")
        }
        if !strings.Contains(html, `name="series_id" value="17360452316"`) {
                t.Errorf("page missing the confirm hidden series_id field")
        }
        if !strings.Contains(html, "MangaUpdates") {
                t.Errorf("page missing the MU credit")
        }
        // A search must not write to the store.
        for _, m := range store.seen {
                if m == "Save" || m == "SaveAll" || m == "Delete" {
                        t.Errorf("search path wrote to store: %s", m)
                }
        }
}

// TestLookupEditConfirmPrefills renders the EDIT form pre-filled from the
// fetched record and proves the stored row (incl. Entry) is untouched.
func TestLookupEditConfirmPrefills(t *testing.T) {
        a, muSrv, store := newTestApp(t, muNarutoFetchBody)
        defer muSrv.Close()

        srv := httptest.NewServer(newServer(a))
        defer srv.Close()

        req, _ := http.NewRequest(http.MethodPost, srv.URL+"/series/s1/lookup/confirm",
                strings.NewReader(urlEncodeForm("series_id", "17360452316")))
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
                t.Fatalf("POST confirm: %v", err)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                t.Fatalf("status = %d, want 200", resp.StatusCode)
        }
        html := string(mustReadAll(t, resp.Body))

        // Mapped values pre-filled into the form fields.
        for _, want := range []string{
                `value="Naruto"`,
                `value="Masashi Kishimoto"`,
                `value="1999"`,
                `value="https://cdn.mangaupdates.com/covers/2/65/naruto.jpg"`,
                `value="https://www.mangaupdates.com/series/7z3yqqk/naruto"`,
                "Filled from",
        } {
                if !strings.Contains(html, want) {
                        t.Errorf("page missing pre-filled value %q", want)
                }
        }
        // The type select marks the mapped value selected.
        if !strings.Contains(html, `<option value="manga" selected`) {
                t.Errorf("type select not pre-selected to manga")
        }
        if !strings.Contains(html, `<option value="completed" selected`) {
                t.Errorf("pub-status select not pre-selected to completed")
        }
        // Existing tags survive (union, not replace): Drama is still rendered.
        if !strings.Contains(html, "Drama") {
                t.Errorf("existing tag lost in the union")
        }
        // Parent select still points at the stored parent.
        if !strings.Contains(html, `value="parent-9" selected`) {
                t.Errorf("parent selection lost")
        }
        // No writes: the confirm re-renders; only Save (via the form) persists.
        for _, m := range store.seen {
                if m == "Save" || m == "SaveAll" || m == "Delete" {
                        t.Errorf("confirm path wrote to store: %s", m)
                }
        }
}

// TestLookupEditSearchMarksCurrent: the edit form's series already points
// at the MU record a search returns — that hit row carries the "current"
// marker (SourceURL slug → id matches the hit's series_id).
func TestLookupEditSearchMarksCurrent(t *testing.T) {
        a, muSrv, _ := newTestApp(t, muNarutoSearchBody)
        defer muSrv.Close()

        srv := httptest.NewServer(newServer(a))
        defer srv.Close()

        req, _ := http.NewRequest(http.MethodPost, srv.URL+"/series/s1/lookup",
                strings.NewReader(urlEncodeForm("query", "naruto")))
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
                t.Fatalf("POST: %v", err)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                t.Fatalf("status = %d, want 200", resp.StatusCode)
        }
        html := string(mustReadAll(t, resp.Body))
        if !strings.Contains(html, `class="mu-current"`) || !strings.Contains(html, "current") {
                t.Errorf("hit row missing the 'current' marker for the already-linked series")
        }
}

// TestLookupUnknownSeries404: a lookup for a non-existent series is a plain
// 404, not a rendered panel (the user is on an invalid URL).
func TestLookupUnknownSeries404(t *testing.T) {
        a, muSrv, _ := newTestApp(t, muNarutoFetchBody)
        defer muSrv.Close()

        srv := httptest.NewServer(newServer(a))
        defer srv.Close()

        req, _ := http.NewRequest(http.MethodPost, srv.URL+"/series/nope/lookup",
                strings.NewReader(urlEncodeForm("query", "x")))
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
                t.Fatalf("POST: %v", err)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusNotFound {
                t.Errorf("status = %d, want 404", resp.StatusCode)
        }
}

// TestLookupBadConfirmIDRendersPanel: a tampered series_id (non-numeric)
// must not 500 — the panel shows the "no series selected" error.
func TestLookupBadConfirmIDRendersPanel(t *testing.T) {
        a, muSrv, _ := newTestApp(t, muNarutoFetchBody)
        defer muSrv.Close()

        srv := httptest.NewServer(newServer(a))
        defer srv.Close()

        req, _ := http.NewRequest(http.MethodPost, srv.URL+"/series/s1/lookup/confirm",
                strings.NewReader(urlEncodeForm("series_id", "not-a-number")))
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
                t.Fatalf("POST: %v", err)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                t.Fatalf("status = %d, want 200 (panel error, not 500)", resp.StatusCode)
        }
        html := string(mustReadAll(t, resp.Body))
        if !strings.Contains(html, "No series selected") {
                t.Errorf("page missing the no-series-selected error")
        }
}

// --- helpers -------------------------------------------------------------------

func urlEncodeForm(kv ...string) string {
        parts := make([]string, 0, len(kv)/2)
        for i := 0; i+1 < len(kv); i += 2 {
                parts = append(parts, kv[i]+"="+url.QueryEscape(kv[i+1]))
        }
        return strings.Join(parts, "&")
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
        t.Helper()
        b, err := io.ReadAll(r)
        if err != nil {
                t.Fatalf("read body: %v", err)
        }
        return b
}
