package main

// mu.go — MangaUpdates metadata lookup (api.mangaupdates.com, v1).
//
// The app's series are usually found on MangaUpdates (MU), and MU's public
// API — the same one that powers the site — serves full series records with
// no authentication for read-only endpoints. This file is the entire
// integration: a stdlib-only HTTP client (search + fetch), a small
// in-process cache so repeated lookups don't re-hammer MU, and the pure
// mapping functions that turn a MU record into fic-tally Series fields.
//
// Design constraints, all deliberate:
//
//   - Stdlib only (net/http, encoding/json). No new dependency.
//   - Sequential requests with a 10s timeout, one retry with backoff on
//     429/5xx, and a descriptive User-Agent — MU's Acceptable Use Policy
//     asks for reasonable spacing, caching, and credit; we do all three.
//   - The mapping is a pure function of (record, existing series, live
//     option lists) with no network, so it is unit-testable in isolation
//     and the rules in the mapping table below are enforced in exactly one
//     place: muApply.
//
//   - fic-tally's core invariant: Series = bibliographic (safe to refresh),
//     Entry = user tracking data (NEVER touched). muApply only ever writes
//     Series fields, and only through the live user-editable option lists —
//     a mapped type/pub-status that the user removed from their vocabulary
//     is left untouched rather than clobbering a row with an orphaned
//     value (same guard as form parsing and import).

import (
        "bytes"
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "net/http"
        "net/url"
        "regexp"
        "strconv"
        "strings"
        "sync"
        "time"
)

const (
        muBaseURL     = "https://api.mangaupdates.com/v1"
        muUserAgent   = "fic-tally/0.1 (+local metadata lookup; personal reading tracker)"
        muHTTPTimeout = 10 * time.Second
        muCacheTTL    = time.Hour
)

// muRetryBackoff is the delay between a 429/5xx and the one retry. A var,
// not a const, so tests can shrink it to stay fast.
var muRetryBackoff = 1500 * time.Millisecond

// --- JSON shapes (mirrors of the MU record fields we consume) --------------

// muAuthor is one entry of a series record's authors array.
type muAuthor struct {
        Name     string `json:"name"`
        AuthorID int64  `json:"author_id"`
        URL      string `json:"url"`
        Type     string `json:"type"` // "Author", "Artist", "Inker", ...
}

// muGenre is one entry of the genres array.
type muGenre struct {
        Genre string `json:"genre"`
}

// muImage is the cover-image block. We only read url.original (full size);
// url.thumb exists but the original is what a library card wants.
type muImage struct {
        URL struct {
                Original string `json:"original"`
                Thumb    string `json:"thumb"`
        } `json:"url"`
}

// muAssocTitle is one associated (alternative/translated) title entry.
type muAssocTitle struct {
        Title string `json:"title"`
}

// muSeriesRecord is a full series record as returned by GET
// /series/{id}?unrenderedFields=true (and embedded, as `record`, in each
// search hit). Only the fields fic-tally maps are declared; the rest of
// the payload is ignored by encoding/json.
//
// Field quirks, verified against the live API:
//   - Year is a STRING ("1999"), not a number.
//   - Status is rendered text ("72 Volumes (Complete)" / "Ongoing").
//   - Completed is bool OR null (json null → nil). Search results can have
//     it null even for finished series, so callers re-fetch the full record
//     before mapping (muApply is the single mapping point).
//   - LatestChapter is the chapter number of the LATEST release. For a
//     single-run finished manga that is the final chapter, but for a
//     multi-volume finished work it is only the end of the final volume
//     ("The Gamer" reports 44 — the end of volume 7 — while its true total
//     is 511; see muTotalChapters). For an ongoing series it is merely the
//     current chapter. It is therefore never stored as a total on its own.
type muSeriesRecord struct {
        SeriesID      int64          `json:"series_id"`
        Title         string         `json:"title"`
        URL           string         `json:"url"`
        Associated    []muAssocTitle `json:"associated"` // alternative/translated titles
        Description   string         `json:"description"`
        Type          string         `json:"type"` // "Manga"/"Manhwa"/"Manhua"/"Novel"/"Doujinshi"/"Webtoon"/...
        Year          string         `json:"year"` // "1999"; "" = unknown
        Status        string         `json:"status"`
        Completed     *bool          `json:"completed"`
        LatestChapter *int           `json:"latest_chapter"`
        Image         muImage        `json:"image"`
        Genres        []muGenre      `json:"genres"`
        Authors       []muAuthor     `json:"authors"`
}

// muSearchResult is one hit of POST /series/search. Record is the FULL
// series record (verified: identical shape to the fetch endpoint) — but
// its `completed` can be null, which is why the confirm step always
// re-fetches before mapping.
type muSearchResult struct {
        Record   *muSeriesRecord `json:"record"`
        HitTitle string          `json:"hit_title"`
}

// muSearchResponse is the envelope of POST /series/search.
type muSearchResponse struct {
        TotalHits int              `json:"total_hits"`
        Page      int              `json:"page"`
        PerPage   int              `json:"per_page"`
        Results   []muSearchResult `json:"results"`
}

// --- client ----------------------------------------------------------------

// muClient talks to api.mangaupdates.com. One per process (see app.go);
// the cache and the http.Client are shared and safe for concurrent use —
// the app is sequential in practice (single user, one lookup at a time),
// but nothing here depends on that.
type muClient struct {
        httpClient *http.Client
        baseURL    string // overridable in tests (httptest)

        mu    sync.Mutex
        cache map[string]muCacheEntry
}

type muCacheEntry struct {
        value   any // []MUSeriesHit or *muSeriesRecord, by key prefix
        expires time.Time
}

// MUSeriesHit is one search result, trimmed to what the results panel
// renders: cover thumb, title, type, year, and the series id that the
// confirm step (and the "current" marker) keys on.
type MUSeriesHit struct {
        SeriesID int64  `json:"series_id"`
        Title    string `json:"title"`
        Type     string `json:"type"`
        Year     string `json:"year"`
        URL      string `json:"url"`
        CoverURL string `json:"cover_url"`
}

// newMUClient builds the client with the production base URL and a
// 10-second-timeout HTTP client (no client-side retry logic — the retry
// lives in doJSON so it applies to both search and fetch).
func newMUClient() *muClient {
        return &muClient{
                httpClient: &http.Client{Timeout: muHTTPTimeout},
                baseURL:    muBaseURL,
                cache:      make(map[string]muCacheEntry),
        }
}

// muSearch queries POST /series/search and returns up to n hits (capped at
// what the API allows per page). Results are cached for muCacheTTL under
// the exact query string.
func (c *muClient) muSearch(query string, n int) ([]MUSeriesHit, error) {
        if n <= 0 {
                n = 10
        }
        key := "search:" + strconv.Itoa(n) + ":" + strings.ToLower(query)
        c.mu.Lock()
        if e, ok := c.cache[key]; ok && time.Now().Before(e.expires) {
                if hits, ok := e.value.([]MUSeriesHit); ok {
                        c.mu.Unlock()
                        return hits, nil
                }
        }
        c.mu.Unlock()

        var resp muSearchResponse
        body, _ := json.Marshal(map[string]any{"search": query, "page": 1, "perpage": n})
        if err := c.doJSON(context.Background(), http.MethodPost, "/series/search", body, &resp); err != nil {
                return nil, err
        }
        hits := make([]MUSeriesHit, 0, len(resp.Results))
        for _, r := range resp.Results {
                if r.Record == nil {
                        continue
                }
                hits = append(hits, MUSeriesHit{
                        SeriesID: r.Record.SeriesID,
                        Title:    r.Record.Title,
                        Type:     r.Record.Type,
                        Year:     r.Record.Year,
                        URL:      r.Record.URL,
                        CoverURL: r.Record.Image.URL.Original,
                })
        }
        c.mu.Lock()
        c.cache[key] = muCacheEntry{value: hits, expires: time.Now().Add(muCacheTTL)}
        c.mu.Unlock()
        return hits, nil
}

// muFetch retrieves the full record for a series id with
// unrenderedFields=true (description arrives as plain text, no **markup**).
// Cached for muCacheTTL per id.
func (c *muClient) muFetch(id int64) (*muSeriesRecord, error) {
        key := "fetch:" + strconv.FormatInt(id, 10)
        c.mu.Lock()
        if e, ok := c.cache[key]; ok && time.Now().Before(e.expires) {
                if rec, ok := e.value.(*muSeriesRecord); ok {
                        c.mu.Unlock()
                        return rec, nil
                }
        }
        c.mu.Unlock()

        var rec muSeriesRecord
        if err := c.doJSON(context.Background(), http.MethodGet,
                "/series/"+strconv.FormatInt(id, 10)+"?unrenderedFields=true", nil, &rec); err != nil {
                return nil, err
        }
        c.mu.Lock()
        c.cache[key] = muCacheEntry{value: &rec, expires: time.Now().Add(muCacheTTL)}
        c.mu.Unlock()
        return &rec, nil
}

// doJSON performs one HTTP request to the MU API and decodes the JSON body
// into out. It retries once after a short backoff on 429 (rate limit) and
// 5xx (server hiccup); every other status is a hard error. The Accept and
// User-Agent headers identify fic-tally as a polite, credit-bearing client.
func (c *muClient) doJSON(ctx context.Context, method, path string, body []byte, out any) error {
        var lastErr error
        for attempt := 0; attempt < 2; attempt++ {
                if attempt > 0 {
                        select {
                        case <-ctx.Done():
                                return ctx.Err()
                        case <-time.After(muRetryBackoff):
                        }
                }
                req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
                if err != nil {
                        return fmt.Errorf("build request: %w", err)
                }
                req.Header.Set("User-Agent", muUserAgent)
                req.Header.Set("Accept", "application/json")
                if body != nil {
                        req.Header.Set("Content-Type", "application/json")
                }

                resp, err := c.httpClient.Do(req)
                if err != nil {
                        lastErr = fmt.Errorf("mangaupdates: %w", err)
                        continue // network failure: worth one retry
                }
                data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
                resp.Body.Close()
                if err != nil {
                        lastErr = fmt.Errorf("mangaupdates: read response: %w", err)
                        continue
                }
                switch {
                case resp.StatusCode == http.StatusOK:
                        if err := json.Unmarshal(data, out); err != nil {
                                return fmt.Errorf("mangaupdates: decode response: %w", err)
                        }
                        return nil
                case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
                        lastErr = fmt.Errorf("mangaupdates: status %d", resp.StatusCode)
                        continue
                default:
                        return fmt.Errorf("mangaupdates: status %d: %s", resp.StatusCode, truncate(string(data), 120))
                }
        }
        return lastErr
}

func truncate(s string, n int) string {
        if len(s) <= n {
                return s
        }
        return s[:n] + "…"
}

// --- base36 series id / slug ------------------------------------------------
//
// MU's human URLs embed the numeric series id as base36:
//   https://www.mangaupdates.com/series/7z3yqqk/naruto  →  17360452316
//
// Storing record.url as the series' SourceURL therefore anchors future
// lookups ("is this series already linked to an MU entry?") with no schema
// change: parse the slug, compare ints.

const base36Digits = "0123456789abcdefghijklmnopqrstuvwxyz"

// slugBase36ToID decodes an MU URL slug ("7z3yqqk") to the numeric series
// id (17360452316). Case-insensitive; empty or non-base36 input is an error.
func slugBase36ToID(slug string) (int64, error) {
        s := strings.ToLower(strings.TrimSpace(slug))
        if s == "" {
                return 0, errors.New("empty slug")
        }
        var v int64
        for _, r := range s {
                idx := strings.IndexRune(base36Digits, r)
                if idx < 0 {
                        return 0, fmt.Errorf("not a base36 slug: %q", slug)
                }
                if v > (mathMaxInt64-9)/36 { // guard against overflow on absurd slugs
                        return 0, errors.New("slug out of range")
                }
                v = v*36 + int64(idx)
        }
        return v, nil
}

// idToSlug encodes a numeric series id (17360452316) to its base36 slug
// ("7z3yqqk"). The inverse of slugBase36ToID for ids in [1, 36^12).
func idToSlug(id int64) string {
        if id <= 0 {
                return ""
        }
        var b []byte
        for id > 0 {
                b = append(b, base36Digits[id%36])
                id /= 36
        }
        for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
                b[i], b[j] = b[j], b[i]
        }
        return string(b)
}

const mathMaxInt64 = int64(^uint64(0) >> 1)

// muIDFromSourceURL extracts the numeric MU series id from a SourceURL, or 0
// if the URL is not an MU series URL. It accepts the canonical shape
// .../series/<slug>/<title> on the mangaupdates.com host (www. or bare) and
// nothing else — a non-MU source URL simply means "not linked yet". The
// host check matters: other sites use the same /series/ path shape, and a
// false positive would mark the wrong series "current".
func muIDFromSourceURL(u string) int64 {
        parsed, err := url.Parse(strings.TrimSpace(u))
        if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" {
                return 0
        }
        switch parsed.Host {
        case "mangaupdates.com", "www.mangaupdates.com":
        default:
                return 0
        }
        p := strings.TrimRight(parsed.Path, "/")
        i := strings.LastIndex(p, "/series/")
        if i < 0 {
                return 0
        }
        rest := p[i+len("/series/"):]
        j := strings.IndexByte(rest, '/')
        if j < 1 {
                return 0
        }
        id, err := slugBase36ToID(rest[:j])
        if err != nil {
                return 0
        }
        return id
}

// --- pure mapping -----------------------------------------------------------

// muTypeMap translates MU's type vocabulary onto fic-tally's canonical type
// values. MU has exotic types (Doujinshi, Webtoon, Malaysian, ...) with no
// built-in here; they map to "" and muApply leaves the existing value
// untouched (the live option-list guard below may still admit a user-added
// custom type, but we never guess).
var muTypeMap = map[string]string{
        "manga":  string(TypeManga),
        "manhwa": string(TypeManhwa),
        "manhua": string(TypeManhua),
        "novel":  string(TypeLightNovel),
}

// muMapType returns fic-tally's type value for a MU type string, or "" when
// there is no built-in equivalent (exotic MU types). Case-insensitive: the
// API has been seen returning both "Manga" and "manga".
func muMapType(muType string) string {
        return muTypeMap[strings.TrimSpace(strings.ToLower(muType))]
}

// muAuthorName picks the author to display: the first entry explicitly
// typed "Author", else the first author at all, else "". MU records list
// the writer first in practice, but the explicit type check is the spec.
func muAuthorName(authors []muAuthor) string {
        for _, a := range authors {
                if strings.EqualFold(a.Type, "Author") {
                        return strings.TrimSpace(a.Name)
                }
        }
        if len(authors) > 0 {
                return strings.TrimSpace(authors[0].Name)
        }
        return ""
}

// muPubStatus derives a fic-tally publication status from a record.
//
//   - completed == true  → "completed" (authoritative flag, wins)
//   - completed == false or null → parse the rendered status text:
//     contains "Ongoing" → ongoing; "Hiatus" → hiatus;
//     "Cancel" or "Discontinu" → cancelled; anything else → "" (unknown).
//
// Returns "" when nothing maps — the caller leaves the existing value
// untouched (same "don't guess" rule as the type mapping).
func muPubStatus(rec *muSeriesRecord) PubStatus {
        if rec.Completed != nil && *rec.Completed {
                return PubCompleted
        }
        s := strings.ToLower(strings.TrimSpace(rec.Status))
        switch {
        case s == "":
                return ""
        case strings.Contains(s, "ongoing"):
                return PubOngoing
        case strings.Contains(s, "hiatus"):
                return PubHiatus
        case strings.Contains(s, "cancel"), strings.Contains(s, "discontinu"):
                return PubCancelled
        default:
                // "72 Volumes (Complete)" with a null completed flag: the text says
                // complete, the flag doesn't. Trust the text in that one direction
                // only — a finished series must not stay "unknown" forever.
                if strings.Contains(s, "complete") {
                        return PubCompleted
                }
                return ""
        }
}

// muYear parses the record's year string to an int (0 = unknown/invalid).
func muYear(rec *muSeriesRecord) int {
        v, err := strconv.Atoi(strings.TrimSpace(rec.Year))
        if err != nil || v <= 0 {
                return 0
        }
        return v
}

// muAltTitles returns the associated (alternative) titles, deduped and
// without the exact main title.
func muAltTitles(rec *muSeriesRecord) []string {
        seen := make(map[string]struct{}, len(rec.Associated))
        out := make([]string, 0, len(rec.Associated))
        for _, a := range rec.Associated {
                t := strings.TrimSpace(a.Title)
                if t == "" || strings.EqualFold(t, rec.Title) {
                        continue
                }
                k := strings.ToLower(t)
                if _, dup := seen[k]; dup {
                        continue
                }
                seen[k] = struct{}{}
                out = append(out, t)
        }
        return out
}

// muTagUnion appends MU genres to the existing tags: existing tags first,
// then each genre not already present (case-insensitive dedupe). User tags
// are never removed — MU data adds to the library's vocabulary, not
// replaces it.
func muTagUnion(existing []string, genres []muGenre) []string {
        out := make([]string, 0, len(existing)+len(genres))
        for _, t := range existing {
                t = strings.TrimSpace(t)
                if t == "" {
                        continue
                }
                out = append(out, t)
        }
        seen := make(map[string]struct{}, len(out)+len(genres))
        for _, t := range out {
                seen[strings.ToLower(t)] = struct{}{}
        }
        for _, g := range genres {
                gt := strings.TrimSpace(g.Genre)
                if gt == "" {
                        continue
                }
                k := strings.ToLower(gt)
                if _, dup := seen[k]; dup {
                        continue
                }
                seen[k] = struct{}{}
                out = append(out, gt)
        }
        return out
}

// muApply merges a fetched MU record into an existing (possibly empty, in
// the add form) Series, honoring every rule in the mapping spec:
//
//	Title         ← always (MU is authoritative for the bibliographic row)
//	AltTitles     ← associated titles (deduped, no exact-title match)
//	Type          ← only if the mapped value exists in the LIVE type options
//	Author        ← first "Author" type entry, else first author
//	Year          ← record year if parseable, else untouched
//	PubStatus     ← completed flag / status text; live-option guard as Type
//	Description   ← plain text (fetched with unrenderedFields=true)
//	CoverURL      ← MU original image, EXCEPT a local upload wins
//	Tags          ← existing ∪ genres (case-insensitive, existing first)
//	SourceURL     ← record.url (anchors future lookups)
//	TotalChapters ← ONLY when completed AND the status text states an
//	                "N Chapters (Complete)" count. A volume-only status
//	                ("72 Volumes (Complete)") has no chapter count → total
//	                left untouched (latest_chapter is a last-release figure,
//	                never a total — see muTotalChapters)
//	ParentID      ← never touched
//
// opts is the CURRENT user-editable option lists (options.go): a mapped
// type or pub-status the user removed from their vocabulary is silently
// dropped from the mapping rather than written as an orphaned value — the
// exact guard readSeriesFromForm applies to form input.

// muTotalChaptersCompleteRe is the editor-maintained chapter count in a
// finished series' status text, e.g. "511 Chapters (Complete)" (The Gamer),
// "200 Chapters + Prologue (Complete)" (Solo Leveling), or
// "243 WN Chapters + 27 SS Chapters (Complete)". MU states this on the
// FIRST line of the status field; it is the authoritative total for
// finished works.
//
// latest_chapter is deliberately NOT used here: it is the chapter number of
// the LAST *release* only. For a multi-volume work that is the final
// volume's chapter (44 for The Gamer, which actually has 511), and for a
// volume-only finished work it is often a small artifact (1, 5, …) — so it
// must never be stored as the total.
var muTotalChaptersCompleteRe = regexp.MustCompile(`(?i)\b(\d[\d,]*)\s+(?:WN\s+|SS\s+)?Chapters\b[^(]*\(Complete`)

// muTotalChapters returns a finished series' total chapter count, or 0 when
// MU's status text does not state one. Only the FIRST line of the status
// text is consulted: MU puts the overall publication status on line 1 and
// per-season / per-volume breakdowns after it ("132 Chapters (Hiatus)\n
// Season 1: 57 Chapters (Complete)"), and a season line would be mistaken
// for the total.
//
// A volume-only status ("72 Volumes (Complete)") has no chapter count, so
// this returns 0 and the caller leaves the existing total untouched rather
// than guessing from latest_chapter.
func muTotalChapters(rec *muSeriesRecord) int {
        status := rec.Status
        if i := strings.IndexByte(status, '\n'); i >= 0 {
                status = status[:i]
        }
        if m := muTotalChaptersCompleteRe.FindStringSubmatch(status); m != nil {
                if n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", "")); err == nil && n > 0 {
                        return n
                }
        }
        return 0
}

func muApply(ser *Series, rec *muSeriesRecord, opts optionLists) {
        ser.Title = strings.TrimSpace(rec.Title)
        ser.AltTitles = muAltTitles(rec)
        ser.SourceURL = strings.TrimSpace(rec.URL)
        ser.Description = strings.TrimSpace(rec.Description)

        if mapped := muMapType(rec.Type); mapped != "" && opts.validType(mapped) {
                ser.Type = SeriesType(mapped)
        }

        if author := muAuthorName(rec.Authors); author != "" {
                ser.Author = author
        }

        if y := muYear(rec); y > 0 {
                ser.Year = y
        }

        if ps := muPubStatus(rec); ps != "" && opts.validPubStatus(string(ps)) {
                ser.PubStatus = ps
        }

        // Cover: the user's own asset wins. A /static/covers/... path is a local
        // upload (see validateCoverURL — the only accepted local prefix); MU's
        // CDN image never displaces it.
        if cover := strings.TrimSpace(rec.Image.URL.Original); cover != "" &&
                !strings.HasPrefix(ser.CoverURL, "/static/covers/") {
                ser.CoverURL = cover
        }

        ser.Tags = muTagUnion(ser.Tags, rec.Genres)

        // Total chapters: only a finished series gets a total. For ongoing
        // series latest_chapter is the CURRENT chapter — storing it as the total
        // would show "700" as final for a work still publishing — so both fields
        // are left exactly as they were. For a finished series the total comes
        // from muTotalChapters: the "N Chapters (Complete)" count in the status
        // text when present, else latest_chapter (volume-only status).
        if rec.Completed != nil && *rec.Completed {
                if total := muTotalChapters(rec); total > 0 {
                        v := float64(total)
                        ser.TotalChapters = &v
                        ser.TotalIsKnown = true
                }
        }
}
