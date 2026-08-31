package main

// transfer.go — bulk data in and out: CSV/JSON import + JSON/CSV export.
//
// Import and export share one column/field vocabulary so a file exported by
// GET /export/csv can be re-imported through POST /import unchanged, and
// /export/json round-trips through /import (JSON) or POST /api/series/batch.
//
// Design notes:
//   - CSV parsing uses encoding/csv (RFC 4180: quoted fields, embedded
//     commas/newlines). The first line MUST be a header containing "title".
//   - The JSON importer accepts either the export envelope
//     {"exported_at":..., "series":[...]} or a bare [...] array.
//   - Duplicate handling is shared with the batch API via resolveImport:
//     exact (normalized-title or id) duplicates follow the policy
//     (skip/update); fuzzy matches are only annotated, never dropped.

import (
        "encoding/csv"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "sort"
        "strconv"
        "strings"
        "time"
)

// csvColumns is the canonical import/export column order.
var csvColumns = []string{
        "title", "alt_titles", "type", "author", "year", "pub_status",
        "description", "tags", "source_url", "cover_url",
        "parent_id", "total_chapters", "total_is_known", "status", "chapter_num",
        "chapter_label", "rating", "notes", "bookmark_url", "bookmark_label",
}

// splitAltTitles splits alternative-title input on newlines, semicolons, or
// pipes — deliberately NOT commas, since titles themselves often contain
// commas (e.g. "Komi-san wa, Comyushou desu").
func splitAltTitles(s string) []string {
        if strings.TrimSpace(s) == "" {
                return nil
        }
        f := strings.FieldsFunc(s, func(r rune) bool {
                return r == '\n' || r == '\r' || r == ';' || r == '|'
        })
        out := make([]string, 0, len(f))
        for _, t := range f {
                if t = strings.TrimSpace(t); t != "" {
                        out = append(out, t)
                }
        }
        return out
}

// stringList is a JSON field that accepts either ["A","B"] or "A; B".
// The JSON export writes arrays; a plain string is accepted on import for
// hand-written payloads. Splits like splitAltTitles.
type stringList []string

// UnmarshalJSON implements the array-or-string flexibility.
func (l *stringList) UnmarshalJSON(b []byte) error {
        var arr []string
        if err := json.Unmarshal(b, &arr); err == nil {
                *l = arr
                return nil
        }
        var s string
        if err := json.Unmarshal(b, &s); err == nil {
                *l = splitAltTitles(s)
                return nil
        }
        return fmt.Errorf("alt_titles must be an array of strings or a single string")
}

// importItem is the wire shape for one series+entry coming from JSON. It
// mirrors the export JSON (json tags of Series+Entry) with two convenience
// aliases: chapter_num (for current_chapter_number) and chapter_number.
type importItem struct {
        ID            string     `json:"id"`
        Title         string     `json:"title"`
        AltTitles     stringList `json:"alt_titles"`
        Type          string     `json:"type"`
        Author        string     `json:"author"`
        Year          *int       `json:"year"`
        PubStatus     string     `json:"pub_status"`
        Description   string     `json:"description"`
        CoverURL      string     `json:"cover_url"`
        Tags          []string   `json:"tags"`
        SourceURL     string     `json:"source_url"`
        ParentID      string     `json:"parent_id"`
        TotalChapters *float64   `json:"total_chapters"`
        TotalIsKnown  bool       `json:"total_is_known"`
        CreatedAt     *time.Time `json:"created_at"`

        Status          Status   `json:"status"`
        ChapterNum      *float64 `json:"current_chapter_number"`
        ChapterNumAlt   *float64 `json:"chapter_num"`
        ChapterNumAlt2  *float64 `json:"chapter_number"`
        ChapterLabel    string   `json:"current_chapter_label"`
        ChapterLabelAlt string   `json:"chapter_label"`
        Rating          *int     `json:"rating"`
        Notes           string   `json:"notes"`
        BookmarkURL     string   `json:"bookmark_url"`
        BookmarkLabel   string   `json:"bookmark_label"`
        LastReadAt      *time.Time `json:"last_read_at"`
}

// toSeriesEntry converts the wire item into the model types, applying the
// same defaults the HTML forms apply (first type option, status=plan to
// read — or whatever the current option lists dictate; see defaultType).
func (it importItem) toSeriesEntry(opts optionLists) (Series, Entry) {
        ser := Series{
                ID:            strings.TrimSpace(it.ID),
                Title:         strings.TrimSpace(it.Title),
                AltTitles:     splitAltTitles(strings.Join(it.AltTitles, "\n")),
                Type:          SeriesType(strings.TrimSpace(it.Type)),
                Author:        strings.TrimSpace(it.Author),
                Year:          yearValue(it.Year),
                PubStatus:     PubStatus(strings.TrimSpace(it.PubStatus)),
                Description:   strings.TrimSpace(it.Description),
                CoverURL:      strings.TrimSpace(it.CoverURL),
                Tags:          it.Tags,
                SourceURL:     strings.TrimSpace(it.SourceURL),
                ParentID:      strings.TrimSpace(it.ParentID),
                TotalChapters: it.TotalChapters,
                TotalIsKnown:  it.TotalIsKnown,
        }
        if ser.Type == "" {
                ser.Type = opts.defaultType()
        }
        if ser.CreatedAt.IsZero() {
                ser.CreatedAt = time.Now().UTC()
        } else if it.CreatedAt != nil {
                ser.CreatedAt = *it.CreatedAt
        }

        ent := Entry{
                Status:              it.Status,
                CurrentChapterNum:   it.ChapterNum,
                CurrentChapterLabel: strings.TrimSpace(it.ChapterLabel),
                Rating:              it.Rating,
                Notes:               strings.TrimSpace(it.Notes),
                BookmarkURL:         strings.TrimSpace(it.BookmarkURL),
                BookmarkLabel:       strings.TrimSpace(it.BookmarkLabel),
        }
        if ent.CurrentChapterNum == nil {
                ent.CurrentChapterNum = it.ChapterNumAlt
                if ent.CurrentChapterNum == nil {
                        ent.CurrentChapterNum = it.ChapterNumAlt2
                }
        }
        if ent.CurrentChapterLabel == "" {
                ent.CurrentChapterLabel = strings.TrimSpace(it.ChapterLabelAlt)
        }
        if ent.Status == "" {
                ent.Status = StatusPlanToRead
        }
        if it.LastReadAt != nil {
                ent.LastReadAt = *it.LastReadAt
        }
        return ser, ent
}

// validateImportItem mirrors the form-level validation, against the CURRENT
// option lists (a value is valid everywhere or nowhere). Returns a message
// for the results table, or "" when the item is acceptable.
func validateImportItem(it importItem, ser Series, ent Entry, opts optionLists) string {
        if strings.TrimSpace(it.Title) == "" {
                return "missing title"
        }
        if !opts.validType(string(ser.Type)) {
                return "unknown type '" + string(ser.Type) + "' (use " + opts.typeValues() + ")"
        }
        if !opts.validStatus(string(ent.Status)) {
                return "unknown status '" + string(ent.Status) + "' (use " + opts.statusValues() + ")"
        }
        if ent.Rating != nil && (*ent.Rating < 1 || *ent.Rating > 10) {
                return "rating must be 1-10"
        }
        if err := validateCoverURL(ser.CoverURL); err != nil {
                return err.Error()
        }
        if !opts.validPubStatus(string(ser.PubStatus)) {
                return "unknown publication status '" + string(ser.PubStatus) + "' (use " + opts.pubStatusValues() + ", or omit)"
        }
        if it.Year != nil && (*it.Year < 1 || *it.Year > 9999) {
                return "year must be between 1 and 9999"
        }
        return ""
}

// yearValue converts the wire's optional year to the model's int (0=unknown).
func yearValue(p *int) int {
        if p == nil {
                return 0
        }
        return *p
}

// parseImportJSON decodes either the export envelope or a bare array.
func parseImportJSON(data []byte) ([]importItem, error) {
        trimmed := strings.TrimSpace(string(data))
        if strings.HasPrefix(trimmed, "[") {
                var items []importItem
                if err := json.Unmarshal(data, &items); err != nil {
                        return nil, fmt.Errorf("JSON array: %w", err)
                }
                return items, nil
        }
        var envelope struct {
                Series []importItem `json:"series"`
        }
        if err := json.Unmarshal(data, &envelope); err != nil {
                return nil, fmt.Errorf("JSON object: %w (expected {\"series\":[...]} or [...])", err)
        }
        if envelope.Series == nil {
                return nil, fmt.Errorf("no \"series\" array found in JSON")
        }
        return envelope.Series, nil
}

// parseImportCSV decodes header-addressed CSV. Unknown columns are ignored;
// missing columns default. The header must contain "title".
func parseImportCSV(data []byte) ([]importItem, error) {
        r := csv.NewReader(strings.NewReader(string(data)))
        r.FieldsPerRecord = -1 // rows may vary; we address by header
        r.TrimLeadingSpace = true

        header, err := r.Read()
        if err != nil {
                return nil, fmt.Errorf("empty CSV: %w", err)
        }
        colIndex := map[string]int{}
        for i, h := range header {
                colIndex[strings.ToLower(strings.TrimSpace(h))] = i
        }
        if _, ok := colIndex["title"]; !ok {
                return nil, fmt.Errorf("CSV header must contain a 'title' column (got: %s)", strings.Join(header, ", "))
        }

        get := func(rec []string, name string) string {
                i, ok := colIndex[name]
                if !ok || i >= len(rec) {
                        return ""
                }
                return strings.TrimSpace(rec[i])
        }
        getFloat := func(rec []string, name string) *float64 {
                raw := get(rec, name)
                if raw == "" {
                        return nil
                }
                v, err := strconv.ParseFloat(raw, 64)
                if err != nil || v < 0 {
                        return nil
                }
                return &v
        }
        getInt := func(rec []string, name string) *int {
                raw := get(rec, name)
                if raw == "" {
                        return nil
                }
                v, err := strconv.Atoi(raw)
                if err != nil || v < 1 || v > 10 {
                        return nil
                }
                return &v
        }
        getBool := func(rec []string, name string) bool {
                switch strings.ToLower(get(rec, name)) {
                case "1", "true", "yes", "on", "y":
                        return true
                }
                return false
        }
        getYear := func(rec []string, name string) *int {
                raw := get(rec, name)
                if raw == "" {
                        return nil
                }
                v, err := strconv.Atoi(raw)
                if err != nil || v < 1 || v > 9999 {
                        return nil
                }
                return &v
        }

        var items []importItem
        line := 1
        for {
                rec, err := r.Read()
                if err == io.EOF {
                        break
                }
                line++
                if err != nil {
                        return nil, fmt.Errorf("CSV row %d: %w", line, err)
                }
                // Skip fully blank rows.
                blank := true
                for _, f := range rec {
                        if strings.TrimSpace(f) != "" {
                                blank = false
                                break
                        }
                }
                if blank {
                        continue
                }
                var tags []string
                for _, t := range splitTags(get(rec, "tags")) {
                        tags = append(tags, t)
                }
                createdAt := time.Time{}
                if raw := get(rec, "created_at"); raw != "" {
                        if t, err := time.Parse(time.RFC3339, raw); err == nil {
                                createdAt = t
                        }
                }
                item := importItem{
                        ID:            get(rec, "id"),
                        Title:         get(rec, "title"),
                        AltTitles:     splitAltTitles(get(rec, "alt_titles")),
                        Type:          get(rec, "type"),
                        Author:        get(rec, "author"),
                        Year:          getYear(rec, "year"),
                        PubStatus:     get(rec, "pub_status"),
                        Description:   get(rec, "description"),
                        CoverURL:      get(rec, "cover_url"),
                        Tags:          tags,
                        SourceURL:     get(rec, "source_url"),
                        ParentID:      get(rec, "parent_id"),
                        TotalChapters: getFloat(rec, "total_chapters"),
                        TotalIsKnown:  getBool(rec, "total_is_known"),
                        CreatedAt:     nil,
                        Status:        Status(get(rec, "status")),
                        ChapterNum:    getFloat(rec, "chapter_num"),
                        ChapterLabel:  get(rec, "chapter_label"),
                        Rating:        getInt(rec, "rating"),
                        Notes:         get(rec, "notes"),
                        BookmarkURL:   get(rec, "bookmark_url"),
                        BookmarkLabel: get(rec, "bookmark_label"),
                }
                if !createdAt.IsZero() {
                        t := createdAt
                        item.CreatedAt = &t
                }
                items = append(items, item)
        }
        return items, nil
}

// splitTags splits a tags cell on any of , ; | so exports from different
// tools re-import cleanly.
func splitTags(s string) []string {
        if strings.TrimSpace(s) == "" {
                return nil
        }
        f := strings.FieldsFunc(s, func(r rune) bool {
                return r == ',' || r == ';' || r == '|'
        })
        out := make([]string, 0, len(f))
        for _, t := range f {
                if t = strings.TrimSpace(t); t != "" {
                        out = append(out, t)
                }
        }
        return out
}

// --- import resolution ------------------------------------------------------

type importAction string

const (
        actionCreate  importAction = "created"
        actionUpdate  importAction = "updated"
        actionSkip    importAction = "skipped"
        actionError   importAction = "error"
        actionDryRun  importAction = "preview"
)

type importResult struct {
        Index  int          `json:"index"`
        Title  string       `json:"title"`
        ID     string       `json:"id,omitempty"`
        Action importAction `json:"action"`
        Message string      `json:"message,omitempty"`
}

type importSummary struct {
        Created int `json:"created"`
        Updated int `json:"updated"`
        Skipped int `json:"skipped"`
        Failed  int `json:"failed"`
}

// resolveImport turns wire items into rows to persist, applying the
// duplicate policy against the existing library. Returns the batch to save
// (empty when dryRun) plus per-item results. existingIDs lets callers pass a
// pre-built id set (the id→item map of everything currently stored).
func resolveImport(items []importItem, existing []EntryWithSeries, policy string, dryRun bool, opts optionLists) ([]SaveItem, []importResult, importSummary) {
        var (
                batch   []SaveItem
                results = make([]importResult, 0, len(items))
                sum     importSummary
        )

        // id → exists, for exact duplicate lookups.
        idSet := map[string]bool{}
        for _, e := range existing {
                idSet[e.ID] = true
        }

        // IDs minted within this batch also count as taken (two rows titled the
        // same in one file must not collide).
        taken := map[string]bool{}

        // findExact locates an existing row whose id matches, OR whose main or
        // alternative titles match any incoming main/alternative title.
        findExact := func(ser Series) *EntryWithSeries {
                if ser.ID != "" {
                        for i := range existing {
                                if existing[i].ID == ser.ID {
                                        return &existing[i]
                                }
                        }
                }
                inNorms := normalizeAll(append([]string{ser.Title}, ser.AltTitles...)...)
                if len(inNorms) == 0 {
                        return nil
                }
                for i := range existing {
                        for _, en := range normalizeAll(append([]string{existing[i].Title}, existing[i].AltTitles...)...) {
                                for _, in := range inNorms {
                                        if in == en {
                                                return &existing[i]
                                        }
                                }
                        }
                }
                return nil
        }

        for i, it := range items {
                ser, ent := it.toSeriesEntry(opts)
                res := importResult{Index: i, Title: ser.Title}

                if msg := validateImportItem(it, ser, ent, opts); msg != "" {
                        res.Action = actionError
                        res.Message = msg
                        sum.Failed++
                        results = append(results, res)
                        continue
                }

                // Resolve the series ID.
                if ser.ID == "" {
                        ser.ID = slugify(ser.Title)
                }
                if ser.ID == "" {
                        res.Action = actionError
                        res.Message = "title has no URL-safe characters"
                        sum.Failed++
                        results = append(results, res)
                        continue
                }

                dup := findExact(ser)
                exists := idSet[ser.ID] || taken[ser.ID]

                action := actionCreate
                switch {
                case dup != nil && policy == "skip":
                        action = actionSkip
                        res.ID = dup.ID
                        res.Message = "duplicate of existing '" + dup.Title + "' — skipped"
                case dup != nil && policy == "update":
                        action = actionUpdate
                        ser.ID = dup.ID // overwrite the existing row
                        ser.CreatedAt = dup.CreatedAt
                        ent.SeriesID = ser.ID
                        res.ID = ser.ID
                        res.Message = "updated existing '" + dup.Title + "'"
                        batch = append(batch, SaveItem{Series: ser, Entry: ent})
                        taken[ser.ID] = true
                case dup != nil && policy == "create":
                        action = actionCreate
                        ser.ID = uniquifyID(ser.ID, idSet, taken)
                        ent.SeriesID = ser.ID
                        res.ID = ser.ID
                        res.Message = "created alongside existing '" + dup.Title + "' (duplicate allowed)"
                        batch = append(batch, SaveItem{Series: ser, Entry: ent})
                        taken[ser.ID] = true
                case exists:
                        // Same ID but a different title: treat as an update of that row.
                        if policy == "skip" {
                                action = actionSkip
                                res.ID = ser.ID
                                res.Message = "id already exists — skipped"
                        } else {
                                action = actionUpdate
                                res.ID = ser.ID
                                res.Message = "updated existing row with this id"
                                batch = append(batch, SaveItem{Series: ser, Entry: ent})
                                taken[ser.ID] = true
                        }
                default:
                        ent.SeriesID = ser.ID
                        res.ID = ser.ID
                        // Advisory note for fuzzy near-matches (never blocks the import).
                        for _, c := range findDuplicates(existing, ser.Title, ser.AltTitles, "") {
                                if c.Strong {
                                        continue // handled above via findExact
                                }
                                res.Message = "note: similar to existing '" + c.Title + "' (" + c.Reason + ")"
                                break
                        }
                        batch = append(batch, SaveItem{Series: ser, Entry: ent})
                        taken[ser.ID] = true
                }

                switch action {
                case actionCreate:
                        sum.Created++
                case actionUpdate:
                        sum.Updated++
                case actionSkip:
                        sum.Skipped++
                }

                if dryRun {
                        res.Action = actionDryRun
                        res.Message = "would " + string(action) + ". " + res.Message
                } else {
                        res.Action = action
                }
                results = append(results, res)
        }

        // Dry-run: report but persist nothing.
        if dryRun {
                batch = nil
        }
        return batch, results, sum
}

// uniquifyID appends -2, -3, ... until the id is free.
func uniquifyID(base string, idSet, taken map[string]bool) string {
        if !idSet[base] && !taken[base] {
                return base
        }
        for n := 2; ; n++ {
                cand := base + "-" + strconv.Itoa(n)
                if !idSet[cand] && !taken[cand] {
                        return cand
                }
        }
}

// --- export -----------------------------------------------------------------

// exportSeries is the JSON export envelope.
type exportEnvelope struct {
        Generator  string            `json:"generator"`
        ExportedAt time.Time         `json:"exported_at"`
        Series     []EntryWithSeries `json:"series"`
}

// buildExportJSON marshals the whole library in the import-compatible shape.
func buildExportJSON(all []EntryWithSeries, now time.Time) ([]byte, error) {
        if all == nil {
                all = []EntryWithSeries{}
        }
        return json.MarshalIndent(exportEnvelope{
                Generator:  "fic-tally",
                ExportedAt: now,
                Series:     all,
        }, "", "  ")
}

// buildExportCSV writes the canonical columns. Tags are comma-joined inside
// a quoted field, which encoding/csv handles and parseImportCSV splits back.
func buildExportCSV(all []EntryWithSeries, now time.Time) ([]byte, error) {
        var sb strings.Builder
        w := csv.NewWriter(&sb)
        if err := w.Write(csvColumns); err != nil {
                return nil, err
        }
        for _, e := range all {
                row := []string{
                        e.Title,
                        strings.Join(e.AltTitles, "; "),
                        string(e.Type),
                        e.Author,
                        intStrOrEmpty(yearPtr(e.Year)),
                        string(e.PubStatus),
                        e.Description,
                        strings.Join(e.Tags, ", "),
                        e.SourceURL,
                        e.CoverURL,
                        e.ParentID,
                        floatStrOrEmpty(e.TotalChapters),
                        boolWord(e.TotalIsKnown),
                        string(e.Status),
                        floatStrOrEmpty(e.CurrentChapterNum),
                        e.CurrentChapterLabel,
                        intStrOrEmpty(e.Rating),
                        e.Notes,
                        e.BookmarkURL,
                        e.BookmarkLabel,
                }
                if err := w.Write(row); err != nil {
                        return nil, err
                }
        }
        w.Flush()
        if err := w.Error(); err != nil {
                return nil, err
        }
        return []byte(sb.String()), nil
}

func floatStrOrEmpty(p *float64) string {
        if p == nil {
                return ""
        }
        return formatChapterNumber(*p)
}

func intStrOrEmpty(p *int) string {
        if p == nil {
                return ""
        }
        return strconv.Itoa(*p)
}

// yearPtr converts the model's int year (0=unknown) to a pointer for
// intStrOrEmpty.
func yearPtr(y int) *int {
        if y == 0 {
                return nil
        }
        return &y
}

func boolWord(b bool) string {
        if b {
                return "true"
        }
        return "false"
}

// --- HTTP handlers ----------------------------------------------------------

// handleImportForm renders the import page (GET /import). When results are
// present in the render data (POST re-render), the template shows them.
// Policy/DryRun are always passed (never nil) because the template's eq
// comparisons can't handle a missing key.
func (a *app) handleImportForm(w http.ResponseWriter, r *http.Request) {
        a.render(w, r, "import.html", map[string]any{
                "Title":  "Batch import",
                "Policy": "",
                "DryRun": false,
        })
}

// handleImportSubmit processes a pasted payload or an uploaded .csv/.json
// file (POST /import), then re-renders the page with a results table.
func (a *app) handleImportSubmit(w http.ResponseWriter, r *http.Request) {
        r.Body = http.MaxBytesReader(w, r.Body, 8<<20)

        var payload []byte
        var source = "pasted text"

        // A multipart request may carry a file; prefer it over the textarea.
        if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
                if err := r.ParseMultipartForm(2 << 20); err != nil {
                        http.Error(w, "upload malformed: "+err.Error(), http.StatusBadRequest)
                        return
                }
                if file, _, err := r.FormFile("file"); err == nil {
                        defer file.Close()
                        data, err := io.ReadAll(file)
                        if err != nil {
                                http.Error(w, "read file: "+err.Error(), http.StatusBadRequest)
                                return
                        }
                        payload = data
                        source = "uploaded file"
                }
        } else if err := r.ParseForm(); err != nil {
                http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
                return
        }
        if payload == nil {
                payload = []byte(r.PostFormValue("payload"))
        }

        policy := r.PostFormValue("dup_policy")
        switch policy {
        case "skip", "update", "create":
        default:
                policy = "skip"
        }
        dryRun := r.PostFormValue("dry_run") == "on"

        if len(strings.TrimSpace(string(payload))) == 0 {
                a.render(w, r, "import.html", map[string]any{
                        "Title":  "Batch import",
                        "Error":  "nothing to import — paste CSV/JSON or choose a file",
                        "Policy": "",
                        "DryRun": false,
                })
                return
        }

        items, format, err := parsePayload(payload)
        if err != nil {
                a.render(w, r, "import.html", map[string]any{
                        "Title":  "Batch import",
                        "Error":  "could not parse input as CSV or JSON: " + err.Error(),
                        "Policy": "",
                        "DryRun": false,
                })
                return
        }
        if len(items) == 0 {
                a.render(w, r, "import.html", map[string]any{
                        "Title":  "Batch import",
                        "Error":  "no rows found in input",
                        "Policy": "",
                        "DryRun": false,
                })
                return
        }
        if len(items) > 1000 {
                a.render(w, r, "import.html", map[string]any{
                        "Title":  "Batch import",
                        "Error":  "too many rows (" + strconv.Itoa(len(items)) + "); max 1000 per request",
                        "Policy": "",
                        "DryRun": false,
                })
                return
        }

        existing, err := a.store.List()
        if err != nil {
                a.serverError(w, r, "list for import", err)
                return
        }

        batch, results, sum := resolveImport(items, existing, policy, dryRun, a.options())
        if !dryRun && len(batch) > 0 {
                if err := a.store.SaveAll(batch); err != nil {
                        a.serverError(w, r, "save import batch", err)
                        return
                }
        }

        a.render(w, r, "import.html", map[string]any{
                "Title":          "Batch import",
                "Results":        results,
                "Summary":        sum,
                "DryRun":         dryRun,
                "ImportedFormat": format,
                "ImportSource":   source,
                "Policy":         policy,
        })
}

// parsePayload auto-detects JSON vs CSV from the first non-space byte.
func parsePayload(data []byte) ([]importItem, string, error) {
        trimmed := strings.TrimLeft(string(data), " \t\r\n")
        if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
                items, err := parseImportJSON(data)
                return items, "JSON", err
        }
        items, err := parseImportCSV(data)
        return items, "CSV", err
}

// handleExportJSON dumps the whole library as the import-compatible envelope.
func (a *app) handleExportJSON(w http.ResponseWriter, r *http.Request) {
        all, err := a.store.List()
        if err != nil {
                a.serverError(w, r, "list for export", err)
                return
        }
        data, err := buildExportJSON(all, time.Now().UTC())
        if err != nil {
                a.serverError(w, r, "marshal export", err)
                return
        }
        w.Header().Set("Content-Type", "application/json; charset=utf-8")
        w.Header().Set("Content-Disposition", `attachment; filename="fic-tally-export-`+time.Now().UTC().Format("20060102")+".json\"")
        w.Write(data)
}

// handleExportCSV dumps the whole library as canonical-column CSV.
func (a *app) handleExportCSV(w http.ResponseWriter, r *http.Request) {
        all, err := a.store.List()
        if err != nil {
                a.serverError(w, r, "list for export", err)
                return
        }
        data, err := buildExportCSV(all, time.Now().UTC())
        if err != nil {
                a.serverError(w, r, "write export csv", err)
                return
        }
        w.Header().Set("Content-Type", "text/csv; charset=utf-8")
        w.Header().Set("Content-Disposition", `attachment; filename="fic-tally-export-`+time.Now().UTC().Format("20060102")+".csv\"")
        w.Write(data)
}

// sortResultsByIndex is used by templates/tests that want stable ordering.
func sortResultsByIndex(rs []importResult) {
        sort.SliceStable(rs, func(i, j int) bool { return rs[i].Index < rs[j].Index })
}