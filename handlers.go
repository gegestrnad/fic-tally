package main

// handlers.go — HTTP request handlers.
//
// Stdlib net/http only, using Go 1.22's enhanced ServeMux patterns
// (method-qualified routes + path wildcards). No chi, no gorilla.
//
// Conventions:
//   - Handlers take a *Store dependency via the app struct (app.go).
//   - Form POSTs use the PRG pattern: validate, mutate, redirect.
//   - Templates are pre-parsed at startup and rendered via render().
//   - All errors bubble through a single 500 handler that logs + renders
//     a simple page. The library is single-user; no error pages need to be
//     polished for strangers.

import (
        "errors"
        "fmt"
        "io"
        "log"
        "net/http"
        "net/url"
        "os"
        "path/filepath"
        "strconv"
        "strings"
        "time"
)

// ctxKey isolates the value-context key namespace (per vet's recommendation).
type ctxKey string

const (
        // sortQueryKey, statusQueryKey etc. are the URL query params the library
        // view reads to filter/sort. They're bookmarkable.
        querySearch = "q"
        queryStatus = "status"
        queryType   = "type"
        queryTag    = "tag"
        querySort   = "sort"
)

// sortOption enumerates the library sort modes. UI<select> values.
type sortOption string

const (
        sortLastRead sortOption = "last_read" // last_read_at desc; nulls last
        sortTitle    sortOption = "title"     // title asc, case-insensitive
        sortRating   sortOption = "rating"    // rating desc, nulls last
        sortUpdated  sortOption = "updated"   // updated_at desc
)

// --- Library list view -----------------------------------------------------

func (a *app) handleLibrary(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
                http.NotFound(w, r)
                return
        }

        all, err := a.store.List()
        if err != nil {
                a.serverError(w, r, "list series", err)
                return
        }

        qRaw := r.URL.Query().Get(querySearch)
        statusFilter := r.URL.Query().Get(queryStatus)
        typeFilter := r.URL.Query().Get(queryType)
        // The search box supports two token kinds, split on whitespace:
        //   #tag tokens — tag filters ("#isekai #romance" = has BOTH tags);
        //   plain tokens — free-text terms, ALL of which must match
        //   ("iron wren" matches Iron Tide by J. Wren, not just the phrase).
        var textTerms, qTags []string
        for _, tok := range strings.Fields(qRaw) {
                if strings.HasPrefix(tok, "#") && len(tok) > 1 {
                        qTags = append(qTags, strings.TrimPrefix(tok, "#"))
                } else if tok != "#" {
                        textTerms = append(textTerms, strings.ToLower(tok))
                }
        }

        // Tag filter param accepts a comma-separated list with AND
        // semantics: ?tag=isekai,romance shows only series carrying both.
        var tagFilters []string
        for _, t := range strings.Split(r.URL.Query().Get(queryTag), ",") {
                if t = strings.TrimSpace(t); t != "" {
                        tagFilters = append(tagFilters, t)
                }
        }

        // Sort: an explicit ?sort= always wins; otherwise the saved default
        // (library settings group), otherwise the built-in default
        // ("updated" — last touched first).
        settings := a.loadUISettings()
        savedSort := DefaultLibraryPrefs().Sort
        if settings.Library != nil && validSortOption(settings.Library.Sort) {
                savedSort = settings.Library.Sort
        }
        sortParam := r.URL.Query().Get(querySort)
        sort := sortOption(sortParam)
        if sortParam == "" || !validSortOption(sortParam) {
                sort = sortOption(savedSort)
        }

        // Filter
        filtered := make([]EntryWithSeries, 0, len(all))
        for _, e := range all {
                if len(textTerms) > 0 || len(qTags) > 0 {
                        hay := strings.ToLower(e.Title + " " + strings.Join(e.AltTitles, " ") + " " +
                                e.Author + " " + strings.Join(e.Tags, " ") + " " + e.Description)
                        ok := true
                        for _, term := range textTerms {
                                if !strings.Contains(hay, term) {
                                        ok = false
                                        break
                                }
                        }
                        if ok {
                                for _, t := range qTags {
                                        if !hasTag(e.Tags, t) {
                                                ok = false
                                                break
                                        }
                                }
                        }
                        if !ok {
                                continue
                        }
                }
                if statusFilter != "" && string(e.Status) != statusFilter {
                        continue
                }
                if typeFilter != "" && string(e.Type) != typeFilter {
                        continue
                }
                if len(tagFilters) > 0 {
                        matched := true
                        for _, t := range tagFilters {
                                if !hasTag(e.Tags, t) {
                                        matched = false
                                        break
                                }
                        }
                        if !matched {
                                continue
                        }
                }
                filtered = append(filtered, e)
        }

        // Sort
        sortFiltered(filtered, sort)

        // Distinct tags for the tag-filter dropdown.
        allTags := a.allTags()

        // For each item, precompute the cover URL the template should use
        // (uploaded image if present, else empty string so the template falls
        // back to the Spectral-serif initial placeholder).
        type cardView struct {
                EntryWithSeries
                CoverSrc     string // "" means: render placeholder initial
                ProgressPct  int    // 0-100, 0 if total unknown
                ChDisplay    string // e.g. "142", "Extra 1", "Vol. 4 Ch. 2"
                TotalDisplay string // e.g. "210+", "96", "—"
                UpdatedRel   string // "2d ago", "just now", "3w ago"
                UpdatedAbs   string // tooltip with the exact timestamp
        }
        cards := make([]cardView, 0, len(filtered))
        for _, e := range filtered {
                cards = append(cards, cardView{
                        EntryWithSeries: e,
                        CoverSrc:        coverSrc(e.Series),
                        ProgressPct:     progressPct(e),
                        ChDisplay:       chDisplay(e),
                        TotalDisplay:    totalDisplay(e.Series),
                        UpdatedRel:      relTime(e.Entry.LastReadAt, time.Now()),
                        UpdatedAbs:      absTime(e.Entry.LastReadAt),
                })
        }

        // Removable chips for every active tag filter — both the ?tag=
        // list and the #tag search tokens — so multi-tag filtering has a
        // visible, clickable state (each chip is a plain link: removing a
        // tag works without JavaScript).
        type tagChip struct {
                Label string
                Href  string
        }
        var chips []tagChip
        for _, t := range tagFilters {
                var others []string
                for _, o := range tagFilters {
                        if !strings.EqualFold(o, t) {
                                others = append(others, o)
                        }
                }
                chips = append(chips, tagChip{
                        Label: t,
                        Href:  filterHref(r, qRaw, strings.Join(others, ",")),
                })
        }
        for _, t := range qTags {
                var kept []string
                for _, tok := range strings.Fields(qRaw) {
                        if !strings.EqualFold(tok, "#"+t) {
                                kept = append(kept, tok)
                        }
                }
                chips = append(chips, tagChip{
                        Label: t,
                        Href:  filterHref(r, strings.Join(kept, " "), strings.Join(tagFilters, ",")),
                })
        }

        // Active tags (both sources) drive the highlighted state of the
        // mini-tags on the cards.
        activeTags := make([]string, 0, len(tagFilters)+len(qTags))
        activeTags = append(activeTags, tagFilters...)
        activeTags = append(activeTags, qTags...)

        filteredView := len(textTerms) > 0 || len(qTags) > 0 || len(tagFilters) > 0 ||
                statusFilter != "" || typeFilter != ""

        // Saved views ("shelves"): render the stored list as chips and mark
        // the one whose canonical params exactly reproduce the current view.
        currentParams, err := a.options().canonicalShelfParams(qRaw, statusFilter, typeFilter,
                r.URL.Query().Get(queryTag), string(sort))
        if err != nil {
                currentParams = "" // unmatchable → no chip highlighted
        }
        shelves := make([]shelfView, 0, len(settings.Shelves))
        for _, sh := range settings.Shelves {
                shelves = append(shelves, shelfView{
                        Name:   sh.Name,
                        Href:   "/" + (map[bool]string{true: "?" + sh.Params, false: ""})[sh.Params != ""],
                        Active: sh.Params == currentParams && currentParams != "",
                })
        }

        a.render(w, r, "library.html", map[string]any{
                "Title":        "Library",
                "Cards":        cards,
                "Query":        qRaw,
                "StatusFilter": statusFilter,
                "TypeFilter":   typeFilter,
                "TagFilter":    r.URL.Query().Get(queryTag),
                "TagFilters":   tagFilters,
                "TagChips":     chips,
                "ActiveTags":   activeTags,
                "Filtered":     filteredView,
                "Sort":         string(sort),
                "SavedSort":    savedSort,
                "AllStatuses":  a.statusOptions(),
                "AllTypes":     a.typeOptions(),
                "AllTags":      allTags,
                "Shelves":      shelves,
                "BackQuery":    r.URL.Query().Encode(), // PRG target for the bulk form
        })
}

// --- Series detail view ----------------------------------------------------

func (a *app) handleDetail(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get series", err)
                return
        }

        // Related series: the parent plus everything whose parent_id is this
        // series (spinoffs/sequels). Fetched with one List() and filtered here —
        // single-user library sizes make a dedicated query unnecessary.
        var parent *EntryWithSeries
        var children []EntryWithSeries
        if item.ParentID != "" {
                if p, err := a.store.Get(item.ParentID); err == nil {
                        parent = p
                }
        }
        if all, err := a.store.List(); err == nil {
                for _, e := range all {
                        if e.ParentID == id {
                                children = append(children, e)
                        }
                }
        }

        // Per-series reading history: the newest entries for the timeline,
        // the count of chapters read in the last 7 days, and (when the
        // total is known and there's enough pace data) a finish estimate.
        history, err := a.store.ChapterLog(id)
        if err != nil {
                log.Printf("[warn] chapter log for %s: %v", id, err) // history is auxiliary
        }
        now := time.Now()
        var logViews []logEntryView
        weekChapters := 0
        weekCutoff := now.AddDate(0, 0, -7)
        for _, l := range history {
                ch := "—"
                if l.Label != "" {
                        ch = l.Label
                } else if l.Chapter != nil {
                        ch = formatChapterNumber(*l.Chapter)
                }
                delta := ""
                switch {
                case l.Delta > 0:
                        delta = "+" + formatChapterNumber(l.Delta)
                case l.Delta < 0:
                        delta = "−" + formatChapterNumber(-l.Delta)
                }
                logViews = append(logViews, logEntryView{
                        When:    l.At.Format("Jan 2"),
                        WhenAbs: l.At.Format("Jan 2, 2006 15:04"),
                        Ch:      ch,
                        Delta:   delta,
                })
                if l.Delta > 0 && !l.At.Before(weekCutoff) {
                        weekChapters += int(l.Delta)
                }
        }
        if len(logViews) > 20 {
                logViews = logViews[:20]
        }
        pace := paceEstimate(*item, history, now)

        a.render(w, r, "detail.html", map[string]any{
                "Title":         item.Title,
                "Item":          item,
                "CoverSrc":      coverSrc(item.Series),
                "ProgressPct":   progressPct(*item),
                "ChDisplay":     chDisplay(*item),
                "TotalDisplay":  totalDisplay(item.Series),
                "LastReadRel":   relTime(item.LastReadAt, time.Now()),
                "LastReadAbs":   absTime(item.LastReadAt),
                "AllStatuses":   a.statusOptions(),
                "AllTypes":      a.typeOptions(),
                "HasCover":      item.CoverURL != "",
                "Parent":        parent,
                "Children":      children,
                "Log":           logViews,
                "WeekChapters":  weekChapters,
                "PaceRate":      pace.rate,
                "PaceDate":      pace.date,
        })
}

// logEntryView is one row of the reading-history list on the detail page.
type logEntryView struct {
        When    string // "Aug 28" (date only — a log is a diary, not a ticker)
        WhenAbs string // full timestamp for the tooltip
        Ch      string // "143" / "Extra 1" / "—" (cleared)
        Delta   string // "+1", "−2", "" for unknown
}

// paceView carries the finish-date estimate shown under the progress bar:
// rate "4.3 ch/wk" and date "Nov 2026". Both empty when there isn't enough
// reading history yet (see paceEstimate).
type paceView struct {
        rate string
        date string
}

// paceEstimate extrapolates a finish date from the recent reading rate.
// Uses the last 14 days of chapter-log entries: chapters read ÷ days
// observed (min 1) × 7 = weekly pace; remaining chapters ÷ pace = weeks
// left. Deliberately conservative about showing a guess — it requires
// positive progress across 2+ distinct days within the window, a known
// total, and current below total; otherwise the UI shows nothing rather
// than a made-up date.
func paceEstimate(item EntryWithSeries, log []ChapterLog, now time.Time) paceView {
        if item.TotalChapters == nil || !item.TotalIsKnown || item.CurrentChapterNum == nil {
                return paceView{}
        }
        remaining := *item.TotalChapters - *item.CurrentChapterNum
        if remaining <= 0 {
                return paceView{}
        }
        cutoff := now.AddDate(0, 0, -14)
        var chapters float64
        var earliest time.Time
        days := map[string]bool{}
        for _, l := range log {
                if l.At.Before(cutoff) {
                        continue
                }
                if l.Delta > 0 {
                        chapters += l.Delta
                        days[l.At.Format("2006-01-02")] = true
                        if earliest.IsZero() || l.At.Before(earliest) {
                                earliest = l.At
                        }
                }
        }
        if chapters <= 0 || len(days) < 2 {
                return paceView{}
        }
        observed := now.Sub(earliest).Hours() / 24
        if observed < 1 {
                observed = 1
        }
        weekly := chapters / observed * 7
        if weekly <= 0 {
                return paceView{}
        }
        weeksLeft := remaining / weekly
        est := now.AddDate(0, 0, int(weeksLeft*7))
        return paceView{
                rate: fmt.Sprintf("%.1f ch/wk", weekly),
                date: est.Format("Jan 2006"),
        }
}

// --- Add / Edit series forms ----------------------------------------------

func (a *app) handleAddForm(w http.ResponseWriter, r *http.Request) {
        a.render(w, r, "edit.html", map[string]any{
                "Title":         "Add series",
                "FormAction":    "/series/new",
                "IsNew":         true,
                "Item":          EntryWithSeries{}, // empty defaults
                "AllStatuses":   a.statusOptions(),
                "AllTypes":      a.typeOptions(),
                "AllPubStatuses": a.pubStatusOptions(),
                "AllSeries":     a.seriesOptions(""),
                "AllTags":       a.allTags(), // feeds the tag autocomplete (data-tags)
                // MU lookup panel (mu_handlers.go): fresh state with the
                // add-form action URLs, so the search box is live on first
                // paint. A lookup POST re-renders this same form with
                // Hits/Error/Applied attached.
                "MU": muEmptyViewModel("/series/new/lookup"),
        })
}

func (a *app) handleEditForm(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get series", err)
                return
        }
        a.render(w, r, "edit.html", map[string]any{
                "Title":         "Edit: " + item.Title,
                "FormAction":    "/series/" + id + "/edit",
                "IsNew":         false,
                "Item":          *item,
                "AllStatuses":   a.statusOptions(),
                "AllTypes":      a.typeOptions(),
                "AllPubStatuses": a.pubStatusOptions(),
                "AllSeries":     a.seriesOptions(id), // every series except the one being edited
                "AllTags":       a.allTags(),          // feeds the tag autocomplete (data-tags)
                "HasCover":      item.CoverURL != "",
                "CoverSrc":      coverSrc(item.Series),
                // MU lookup panel (mu_handlers.go): fresh state with the
                // per-series action URLs, so the search box is live on
                // first paint; MUCurrentID is set by muFormBase for the
                // lookup re-renders and marks the series' current link.
                "MU": muEmptyViewModel("/series/" + id + "/lookup"),
        })
}

// seriesOptions lists (id, title) pairs for the parent-series select,
// excluding excludeID so a series can't be its own parent.
func (a *app) seriesOptions(excludeID string) []struct{ ID, Title string } {
        all, err := a.store.List()
        if err != nil {
                log.Printf("[error] list for parent options: %v", err)
                return nil
        }
        out := make([]struct{ ID, Title string }, 0, len(all))
        for _, e := range all {
                if e.ID == excludeID {
                        continue
                }
                out = append(out, struct{ ID, Title string }{e.ID, e.Title})
        }
        return out
}

// allTags returns the distinct tags across the library, case-insensitively
// sorted. Feeds the tag-filter dropdown on the library page and the
// autocomplete suggestion list on the edit form (via the jsonTags template
// func → data-tags attribute).
func (a *app) allTags() []string {
        all, err := a.store.List()
        if err != nil {
                log.Printf("[error] list for tags: %v", err)
                return nil
        }
        tagSet := map[string]struct{}{}
        for _, e := range all {
                for _, t := range e.Tags {
                        tagSet[t] = struct{}{}
                }
        }
        out := make([]string, 0, len(tagSet))
        for t := range tagSet {
                out = append(out, t)
        }
        sortStringsCaseInsensitive(out)
        return out
}

// --- POST handlers ---------------------------------------------------------

func (a *app) handleAddSubmit(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse form", err)
                return
        }
        ser, ent, err := a.readSeriesFromForm(r, true)
        if err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }

        // Duplicate check: on a fuzzy/exact hit, bounce the form back with a
        // warning unless the user explicitly confirmed (dup_confirm=1 comes from
        // the "Save anyway" button rendered alongside the warning).
        if r.PostForm.Get("dup_confirm") != "1" {
                existing, err := a.store.List()
                if err != nil {
                        a.serverError(w, r, "list for dup check", err)
                        return
                }
                if dups := findDuplicates(existing, ser.Title, ser.AltTitles, ""); len(dups) > 0 {
                        formVals := r.PostForm
                        a.render(w, r, "edit.html", map[string]any{
                                "Title":         "Add series",
                                "FormAction":     "/series/new",
                                "IsNew":          true,
                                "Item":           rebuildFormViewModel(formVals),
                                "AllStatuses":    a.statusOptions(),
                                "AllTypes":       a.typeOptions(),
                                "AllPubStatuses": a.pubStatusOptions(),
                                "AllSeries":      a.seriesOptions(""),
                                "AllTags":        a.allTags(),
                                "DupWarning":     dups,
                        })
                        return
                }
        }

        if err := a.validateParent(ser.ParentID, ""); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        if err := a.store.Save(ser, ent, false); err != nil {
                a.serverError(w, r, "save new", err)
                return
        }
        http.Redirect(w, r, "/series/"+ser.ID, http.StatusSeeOther)
}

func (a *app) handleEditSubmit(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse form", err)
                return
        }
        ser, _, err := a.readSeriesFromForm(r, false)
        if err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        ser.ID = id
        // The metadata edit form carries NO entry fields (status, chapter,
        // rating, notes, bookmarks are edited on the detail page), so the
        // entry built from the form holds only defaults. Replace it wholesale
        // with the stored entry — the edit page's promise is "metadata changes
        // don't touch your tracking data", and that's only true if we don't
        // persist those defaults. Only LastReadAt preservation was needed
        // before; keeping the entire entry is both simpler and correct.
        existing, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "load for edit", err)
                return
        }
        if err := a.validateParent(ser.ParentID, id); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        if err := a.store.Save(ser, existing.Entry, false); err != nil {
                a.serverError(w, r, "save edit", err)
                return
        }
        http.Redirect(w, r, "/series/"+id, http.StatusSeeOther)
}

// validateParent rejects parent ids that don't exist (or equal self).
func (a *app) validateParent(parentID, selfID string) error {
        if parentID == "" {
                return nil
        }
        if parentID == selfID {
                return fmt.Errorf("a series can't be its own parent")
        }
        if _, err := a.store.Get(parentID); err != nil {
                return fmt.Errorf("parent series %q not found", parentID)
        }
        return nil
}

// rebuildFormViewModel reconstructs an EntryWithSeries from the submitted
// form values so the dup-warning re-render keeps everything the user typed.
func rebuildFormViewModel(vals map[string][]string) EntryWithSeries {
        get := func(k string) string {
                if v, ok := vals[k]; ok && len(v) > 0 {
                        return v[0]
                }
                return ""
        }
        item := EntryWithSeries{}
        item.Title = get("title")
        item.AltTitles = splitAltTitles(get("alt_titles"))
        item.Type = SeriesType(get("type"))
        if item.Type == "" {
                item.Type = TypeManga
        }
        item.Author = get("author")
        if raw := get("year"); raw != "" {
                if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 9999 {
                        item.Year = v
                }
        }
        item.PubStatus = PubStatus(get("pub_status"))
        item.Description = get("description")
        item.SourceURL = get("source_url")
        item.CoverURL = get("cover_url")
        item.ParentID = get("parent_id")
        item.Tags = splitTags(get("tags"))
        if raw := get("total_chapters"); raw != "" {
                if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
                        item.TotalChapters = &v
                }
        }
        item.TotalIsKnown = get("total_is_known") == "on"
        item.Status = Status(get("status"))
        if item.Status == "" {
                item.Status = StatusPlanToRead
        }
        if raw := get("chapter_num"); raw != "" {
                if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
                        item.CurrentChapterNum = &v
                }
        }
        item.CurrentChapterLabel = get("chapter_label")
        if raw := get("rating"); raw != "" {
                if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 10 {
                        item.Rating = &v
                }
        }
        item.Notes = get("notes")
        item.BookmarkURL = get("bookmark_url")
        item.BookmarkLabel = get("bookmark_label")
        return item
}

func (a *app) handleProgress(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse form", err)
                return
        }
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get for progress", err)
                return
        }
        oldNum := item.CurrentChapterNum

        // Three submit modes (single form, name-based dispatch):
        //   - btn_plus      → advance current_chapter_number by 1
        //   - btn_set       → set current_chapter_number to the typed value
        //   - btn_clear_num → unset current_chapter_number (label-only tracking)
        var newNum *float64
        advance := false
        switch {
        case r.PostForm.Get("btn_plus") != "":
                if item.CurrentChapterNum != nil {
                        v := *item.CurrentChapterNum + 1
                        newNum = &v
                } else {
                        v := 1.0
                        newNum = &v
                }
                advance = true
        case r.PostForm.Get("btn_set") != "":
                raw := strings.TrimSpace(r.PostForm.Get("chapter_set"))
                if raw != "" {
                        v, err := strconv.ParseFloat(raw, 64)
                        if err == nil && v >= 0 {
                                newNum = &v
                                // Advance only if the new value is greater than the old.
                                if item.CurrentChapterNum == nil || v > *item.CurrentChapterNum {
                                        advance = true
                                }
                        }
                }
        case r.PostForm.Get("btn_clear_num") != "":
                newNum = nil
                // Doesn't count as advancing.
        }

        // The chapter label always mirrors the numeric value on the progress
        // path — there is no label field on this form (the edit form is the
        // one place custom labels like "Extra 1" are entered). Deriving it
        // from newNum on every submit means the label can never drift behind
        // the number: previously the label came from a hidden input echoing
        // the page-load value, so a +1/Set on an already-labelled entry kept
        // re-saving the stale label (card showed "71" while num was 100).
        var label string
        if newNum != nil {
                label = formatChapterNumber(*newNum)
        }

        item.CurrentChapterNum = newNum
        item.CurrentChapterLabel = label
        if err := a.store.Save(item.Series, item.Entry, advance); err != nil {
                a.serverError(w, r, "save progress", err)
                return
        }

        // Reading history: log the update only when the numeric chapter
        // actually moved (+1, Set to a different value, Clear). The delta is
        // signed so setbacks show as negatives; "chapters this week" and the
        // pace estimate only ever count positive deltas. A logging failure
        // is logged but non-fatal — the tracker's primary state already
        // saved, and history is an auxiliary view.
        if !sameNumPtr(oldNum, newNum) {
                var delta float64
                if oldNum != nil {
                        delta -= *oldNum
                }
                if newNum != nil {
                        delta += *newNum
                }
                if err := a.store.AppendLog(id, newNum, label, delta); err != nil {
                        log.Printf("[warn] append chapter log for %s: %v", id, err)
                }
        }
        http.Redirect(w, r, "/series/"+id, http.StatusSeeOther)
}

// sameNumPtr compares two *float64 values including the nil cases, so the
// progress handler can detect "did the chapter actually change?".
func sameNumPtr(a, b *float64) bool {
        if a == nil || b == nil {
                return a == b
        }
        return *a == *b
}

func (a *app) handleEntryEdit(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse form", err)
                return
        }
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get for entry edit", err)
                return
        }

        // Only Entry fields (status, rating, notes, bookmark) come from this
        // form. Series fields are untouched. The status is validated against
        // the current option list (the select only offers valid values, but a
        // crafted POST must not store an unknown one — the stats tiles and
        // filters would silently miss it).
        item.Status = Status(r.PostForm.Get("status"))
        if item.Status == "" {
                item.Status = StatusPlanToRead
        }
        if !a.options().validStatus(string(item.Status)) {
                http.Error(w, fmt.Sprintf("unknown status %q (use %s)", string(item.Status), a.options().statusValues()), http.StatusBadRequest)
                return
        }
        if raw := strings.TrimSpace(r.PostForm.Get("rating")); raw != "" {
                if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 10 {
                        item.Rating = &v
                }
        } else {
                item.Rating = nil // cleared
        }
        item.Notes = strings.TrimSpace(r.PostForm.Get("notes"))
        item.BookmarkURL = strings.TrimSpace(r.PostForm.Get("bookmark_url"))
        item.BookmarkLabel = strings.TrimSpace(r.PostForm.Get("bookmark_label"))

        if err := a.store.Save(item.Series, item.Entry, false); err != nil {
                a.serverError(w, r, "save entry", err)
                return
        }
        http.Redirect(w, r, "/series/"+id, http.StatusSeeOther)
}

// handleBulkStatus serves POST /bulk/status — the bulk action bar on the
// library page. Every card carries a checkbox (outside the card's <a>, so
// ticking it never navigates); submitting applies the chosen status to all
// selected series in one go. Pure server-side forms: no JavaScript is
// involved (JS only live-updates the "N selected" counter). The per-series
// Save path is reused verbatim, so completed_at transitions and updated_at
// bumps behave exactly like a single-series edit from the detail page.
func (a *app) handleBulkStatus(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse bulk form", err)
                return
        }
        status := r.PostFormValue("status")
        if status == "" || !a.options().validStatus(status) {
                http.Error(w, "pick a status to apply", http.StatusBadRequest)
                return
        }
        for _, id := range r.PostForm["series_ids"] {
                item, err := a.store.Get(id)
                if err != nil {
                        if errors.Is(err, ErrNotFound) {
                                continue // deleted in another tab — not worth failing the batch
                        }
                        a.serverError(w, r, "bulk get "+id, err)
                        return
                }
                item.Status = Status(status)
                if err := a.store.Save(item.Series, item.Entry, false); err != nil {
                        a.serverError(w, r, "bulk save "+id, err)
                        return
                }
        }
        // PRG back to the exact view the user was looking at (the bulk form
        // carries the current query as a hidden "back" field — the form
        // itself POSTs to /bulk/status with no query string).
        back := r.PostFormValue("back")
        if back != "" && strings.HasPrefix(back, "?") {
                http.Redirect(w, r, "/"+back, http.StatusSeeOther)
                return
        }
        http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleCoverUpload receives a multipart upload at POST /series/{id}/cover
// and stores it under static/covers/<id>.<ext>. Updates Series.CoverURL.
func (a *app) handleCoverUpload(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        // Hard cap at 8 MiB. Covers are small images; a high-res phone
        // photo rarely exceeds 5 MiB. We use MaxBytesReader so the limit is
        // enforced up-front (rather than the stdlib's maxMemory + 10 MiB
        // spill-to-disk ceiling), and so we can distinguish "too large"
        // from "malformed" in the error response.
        r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
        if err := r.ParseMultipartForm(2 << 20); err != nil {
                var maxErr *http.MaxBytesError
                if errors.As(err, &maxErr) {
                        http.Error(w, "cover image exceeds 8 MiB; please use a smaller image", http.StatusRequestEntityTooLarge)
                        return
                }
                http.Error(w, "upload malformed: "+err.Error(), http.StatusBadRequest)
                return
        }
        file, _, err := r.FormFile("cover")
        if err != nil {
                http.Error(w, "no file uploaded (expected a 'cover' field)", http.StatusBadRequest)
                return
        }
        defer file.Close()

        // Sniff content type from the first 512 bytes — never trust the
        // filename extension or the client-provided Content-Type.
        buf := make([]byte, 512)
        n, _ := file.Read(buf)
        contentType := http.DetectContentType(buf[:n])
        if !strings.HasPrefix(contentType, "image/") {
                http.Error(w, "uploaded file is not an image (detected "+contentType+")", http.StatusUnsupportedMediaType)
                return
        }
        if _, err := file.Seek(0, 0); err != nil {
                a.serverError(w, r, "seek upload", err)
                return
        }

        ext := ".jpg"
        switch contentType {
        case "image/png":
                ext = ".png"
        case "image/gif":
                ext = ".gif"
        case "image/webp":
                ext = ".webp"
        }

        // Filename = series ID + ext. Series IDs are slug-safe (enforced in
        // readSeriesFromForm), so this is path-injection-safe.
        filename := id + ext
        dstPath := filepath.Join(a.coverDir, filename)
        out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
        if err != nil {
                a.serverError(w, r, "create cover file", err)
                return
        }
        defer out.Close()
        if _, err := io.Copy(out, file); err != nil {
                a.serverError(w, r, "write cover file", err)
                return
        }

        // Update the Series row's cover_url. We don't touch any Entry fields,
        // so we re-load the existing entry and pass it back through Save.
        item, err := a.store.Get(id)
        if err != nil {
                a.serverError(w, r, "get for cover update", err)
                return
        }
        item.Series.CoverURL = "/static/covers/" + filename
        if err := a.store.Save(item.Series, item.Entry, false); err != nil {
                a.serverError(w, r, "save cover url", err)
                return
        }
        http.Redirect(w, r, "/series/"+id+"/edit", http.StatusSeeOther)
}

// handleCoverURLSet sets the cover to a remote URL (POST /series/{id}/cover/url).
// Complements the file upload: same Series.CoverURL field, just sourced from
// a pasted http(s) link instead of multipart bytes. If the previous cover was
// an uploaded file, that file is removed so we don't leak orphans.
func (a *app) handleCoverURLSet(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        if err := r.ParseForm(); err != nil {
                http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
                return
        }
        rawURL := strings.TrimSpace(r.PostForm.Get("cover_url"))
        if err := validateCoverURL(rawURL); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get for cover url", err)
                return
        }
        // Clean up a previously uploaded file when switching to a remote URL.
        if item.CoverURL != "" && strings.HasPrefix(item.CoverURL, "/static/covers/") {
                _ = os.Remove(filepath.Join(a.coverDir, filepath.Base(item.CoverURL)))
        }
        item.Series.CoverURL = rawURL
        if err := a.store.Save(item.Series, item.Entry, false); err != nil {
                a.serverError(w, r, "save cover url", err)
                return
        }
        http.Redirect(w, r, "/series/"+id+"/edit", http.StatusSeeOther)
}

func (a *app) handleCoverDelete(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get for cover delete", err)
                return
        }
        // Best-effort file removal — the DB update is what matters; an orphan
        // image file under static/covers/ is harmless.
        if item.CoverURL != "" {
                _ = os.Remove(filepath.Join(a.coverDir, filepath.Base(item.CoverURL)))
                item.Series.CoverURL = ""
                if err := a.store.Save(item.Series, item.Entry, false); err != nil {
                        a.serverError(w, r, "save cover delete", err)
                        return
                }
        }
        http.Redirect(w, r, "/series/"+id+"/edit", http.StatusSeeOther)
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        // Drop the cover file too, if present.
        if item, err := a.store.Get(id); err == nil && item.CoverURL != "" {
                _ = os.Remove(filepath.Join(a.coverDir, filepath.Base(item.CoverURL)))
        }
        if err := a.store.Delete(id); err != nil {
                a.serverError(w, r, "delete", err)
                return
        }
        http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- helpers shared across handlers ---------------------------------------

// validateCoverURL accepts "" (no cover), remote http(s) URLs, and local
// /static/covers/ paths set by the upload handler. Anything else — notably
// javascript:/data: URLs — is rejected up-front. html/template would also
// sanitize unsafe src values (ZgotmplZ), but failing loudly at save time is
// friendlier than silently rendering a broken image later.
func validateCoverURL(s string) error {
        if s == "" {
                return nil
        }
        if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "/static/covers/") {
                return nil
        }
        return fmt.Errorf("cover URL must start with http://, https://, or /static/covers/")
}

// readSeriesFromForm parses the shared add/edit metadata form. It is a
// method on app because type, reading-status and publication-status values
// are validated against the CURRENT user-editable option lists — a value is
// valid everywhere (form, import, API, filters) or nowhere. Labels never
// enter the picture: forms post canonical values, and the selects render
// them with the (renamable) labels.
func (a *app) readSeriesFromForm(r *http.Request, isNew bool) (Series, Entry, error) {
        opts := a.options()
        ser := Series{
                Title:       strings.TrimSpace(r.PostForm.Get("title")),
                AltTitles:   splitAltTitles(r.PostForm.Get("alt_titles")),
                Type:        SeriesType(r.PostForm.Get("type")),
                Author:      strings.TrimSpace(r.PostForm.Get("author")),
                PubStatus:   PubStatus(strings.TrimSpace(r.PostForm.Get("pub_status"))),
                Description: strings.TrimSpace(r.PostForm.Get("description")),
                SourceURL:   strings.TrimSpace(r.PostForm.Get("source_url")),
                CoverURL:    strings.TrimSpace(r.PostForm.Get("cover_url")),
                ParentID:    strings.TrimSpace(r.PostForm.Get("parent_id")),
        }
        if ser.Type == "" {
                ser.Type = opts.defaultType()
        }
        if err := validateCoverURL(ser.CoverURL); err != nil {
                return Series{}, Entry{}, err
        }
        if !opts.validType(string(ser.Type)) {
                return Series{}, Entry{}, fmt.Errorf("unknown type %q (use %s)", string(ser.Type), opts.typeValues())
        }
        if !opts.validPubStatus(string(ser.PubStatus)) {
                return Series{}, Entry{}, fmt.Errorf("unknown publication status %q (use %s, or leave it empty)", string(ser.PubStatus), opts.pubStatusValues())
        }
        // Released year: optional; blank stays 0 (unknown). Bad input is an
        // error rather than a silent default so typos don't vanish.
        if raw := strings.TrimSpace(r.PostForm.Get("year")); raw != "" {
                v, err := strconv.Atoi(raw)
                if err != nil || v < 1 || v > 9999 {
                        return Series{}, Entry{}, fmt.Errorf("year must be a number between 1 and 9999 (got %q)", raw)
                }
                ser.Year = v
        }
        if isNew {
                ser.ID = slugify(ser.Title)
                if ser.ID == "" {
                        return Series{}, Entry{}, fmt.Errorf("title must not be empty")
                }
                ser.CreatedAt = time.Now().UTC()
        } else {
                // Existing record: preserve CreatedAt via a hidden form field.
                ser.CreatedAt = time.Now().UTC()
                if raw := r.PostForm.Get("created_at"); raw != "" {
                        if t, err := time.Parse(time.RFC3339, raw); err == nil {
                                ser.CreatedAt = t
                        }
                }
        }

        // Tags: comma-separated text → string slice.
        tagsRaw := strings.TrimSpace(r.PostForm.Get("tags"))
        if tagsRaw != "" {
                parts := strings.Split(tagsRaw, ",")
                for _, p := range parts {
                        if t := strings.TrimSpace(p); t != "" {
                                ser.Tags = append(ser.Tags, t)
                        }
                }
        }

        // total_chapters + total_is_known
        if raw := strings.TrimSpace(r.PostForm.Get("total_chapters")); raw != "" {
                if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
                        ser.TotalChapters = &v
                }
        }
        // total_is_known is a checkbox: present = true.
        ser.TotalIsKnown = r.PostForm.Get("total_is_known") == "on"

        ent := Entry{
                SeriesID:            ser.ID,
                Status:              Status(r.PostForm.Get("status")),
                CurrentChapterLabel: strings.TrimSpace(r.PostForm.Get("chapter_label")),
                Notes:               strings.TrimSpace(r.PostForm.Get("notes")),
                BookmarkURL:         strings.TrimSpace(r.PostForm.Get("bookmark_url")),
                BookmarkLabel:       strings.TrimSpace(r.PostForm.Get("bookmark_label")),
        }
        if ent.Status == "" {
                ent.Status = StatusPlanToRead
        }
        if !opts.validStatus(string(ent.Status)) {
                return Series{}, Entry{}, fmt.Errorf("unknown status %q (use %s)", string(ent.Status), opts.statusValues())
        }
        if raw := strings.TrimSpace(r.PostForm.Get("chapter_num")); raw != "" {
                if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
                        ent.CurrentChapterNum = &v
                }
        }
        if raw := strings.TrimSpace(r.PostForm.Get("rating")); raw != "" {
                if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 10 {
                        ent.Rating = &v
                }
        }
        return ser, ent, nil
}

// hasTag reports whether the tag list contains want, case-insensitively
// (tag filtering is case-insensitive everywhere: ?tag=, #tokens, and the
// highlighted mini-tags on cards).
func hasTag(tags []string, want string) bool {
        for _, t := range tags {
                if strings.EqualFold(t, want) {
                        return true
                }
        }
        return false
}

// filterHref builds a library URL from the given search and tag values while
// preserving the request's status/type/sort params. Used for the removable
// tag chips (and the "Clear filters" empty state) so removing one filter
// never throws away the others. Empty values are omitted; a fully empty
// result is the plain library page.
func filterHref(r *http.Request, q, tag string) string {
        v := url.Values{}
        if q != "" {
                v.Set(querySearch, q)
        }
        if tag != "" {
                v.Set(queryTag, tag)
        }
        if s := r.URL.Query().Get(queryStatus); s != "" {
                v.Set(queryStatus, s)
        }
        if t := r.URL.Query().Get(queryType); t != "" {
                v.Set(queryType, t)
        }
        if s := r.URL.Query().Get(querySort); s != "" {
                v.Set(querySort, s)
        }
        if len(v) == 0 {
                return "/"
        }
        return "/?" + v.Encode()
}

// sortFiltered orders the slice per the requested sortOption. All sorts
// fall back to title-asc for ties so the order is deterministic.
func sortFiltered(items []EntryWithSeries, by sortOption) {
        switch by {
        case sortTitle:
                insertionSort(items, func(a, b EntryWithSeries) bool {
                        return strings.ToLower(a.Title) < strings.ToLower(b.Title)
                })
        case sortRating:
                insertionSort(items, func(a, b EntryWithSeries) bool {
                        ar, br := 0, 0
                        if a.Rating != nil {
                                ar = *a.Rating
                        }
                        if b.Rating != nil {
                                br = *b.Rating
                        }
                        if ar != br {
                                return ar > br
                        }
                        return strings.ToLower(a.Title) < strings.ToLower(b.Title)
                })
        case sortUpdated:
                insertionSort(items, func(a, b EntryWithSeries) bool {
                        if !a.UpdatedAt.Equal(b.UpdatedAt) {
                                return a.UpdatedAt.After(b.UpdatedAt)
                        }
                        return strings.ToLower(a.Title) < strings.ToLower(b.Title)
                })
        case sortLastRead:
                insertionSort(items, func(a, b EntryWithSeries) bool {
                        // Treat zero time as "never"; never sorts below any real time.
                        aNever := a.LastReadAt.IsZero()
                        bNever := b.LastReadAt.IsZero()
                        if aNever && bNever {
                                return strings.ToLower(a.Title) < strings.ToLower(b.Title)
                        }
                        if aNever {
                                return false
                        }
                        if bNever {
                                return true
                        }
                        if !a.LastReadAt.Equal(b.LastReadAt) {
                                return a.LastReadAt.After(b.LastReadAt)
                        }
                        return strings.ToLower(a.Title) < strings.ToLower(b.Title)
                })
        }
}

// insertionSort is used instead of sort.Slice because slices in this app
// are tiny (single-user library, hundreds at most) and insertion sort
// preserves stability for free. The cost is O(n²) but n is small.
func insertionSort(items []EntryWithSeries, less func(a, b EntryWithSeries) bool) {
        for i := 1; i < len(items); i++ {
                for j := i; j > 0 && less(items[j], items[j-1]); j-- {
                        items[j], items[j-1] = items[j-1], items[j]
                }
        }
}

func sortStringsCaseInsensitive(s []string) {
        for i := 1; i < len(s); i++ {
                for j := i; j > 0 && strings.ToLower(s[j]) < strings.ToLower(s[j-1]); j-- {
                        s[j], s[j-1] = s[j-1], s[j]
                }
        }
}

// --- view-model helpers ---------------------------------------------------

// coverSrc returns the URL the <img> should use, or "" if no cover is set
// (in which case the template renders the initial-letter placeholder).
func coverSrc(s Series) string {
        if s.CoverURL == "" {
                return ""
        }
        return s.CoverURL
}

// progressPct returns 0-100. Returns 0 if total_chapters is nil (unknown)
// or if current is 0. Caps at 100 if current > total.
func progressPct(e EntryWithSeries) int {
        if e.TotalChapters == nil || *e.TotalChapters <= 0 {
                return 0
        }
        if e.CurrentChapterNum == nil || *e.CurrentChapterNum <= 0 {
                return 0
        }
        pct := int((*e.CurrentChapterNum / *e.TotalChapters) * 100)
        if pct < 0 {
                pct = 0
        }
        if pct > 100 {
                pct = 100
        }
        return pct
}

// chDisplay returns the chapter label as shown in the UI. Falls back to the
// numeric value formatted as a string if label is empty.
func chDisplay(e EntryWithSeries) string {
        if e.CurrentChapterLabel != "" {
                return e.CurrentChapterLabel
        }
        if e.CurrentChapterNum != nil {
                return formatChapterNumber(*e.CurrentChapterNum)
        }
        return "—"
}

// totalDisplay formats the total chapters per spec: "210+" if total_is_known
// is false, "96" if known, "—" if nil.
func totalDisplay(s Series) string {
        if s.TotalChapters == nil {
                return "—"
        }
        num := formatChapterNumber(*s.TotalChapters)
        if !s.TotalIsKnown {
                return num + "+"
        }
        return num
}

// formatChapterNumber formats a float chapter number with no trailing
// ".0" (so 142.0 → "142", 142.5 → "142.5", 96.0 → "96").
func formatChapterNumber(v float64) string {
        if v == float64(int(v)) {
                return strconv.Itoa(int(v))
        }
        return strconv.FormatFloat(v, 'f', -1, 64)
}

// absTime formats t for a tooltip: the exact date+time behind a relative
// "2d ago" string. Empty for zero time (never read) so the template can
// skip the title attribute entirely.
func absTime(t time.Time) string {
        if t.IsZero() {
                return ""
        }
        return t.Format("Jan 2, 2006 15:04")
}

// relTime returns a compact relative-time string like "2d ago" or "5w ago".
// Returns "—" for zero time (never read). Caps at weeks to avoid implying
// false precision; for >= 1 year, shows "1y ago" etc.
func relTime(t time.Time, now time.Time) string {
        if t.IsZero() {
                return "—"
        }
        d := now.Sub(t)
        if d < 0 {
                d = 0
        }
        switch {
        case d < time.Hour:
                m := int(d / time.Minute)
                if m < 1 {
                        return "just now"
                }
                return strconv.Itoa(m) + "m ago"
        case d < 24*time.Hour:
                h := int(d / time.Hour)
                return strconv.Itoa(h) + "h ago"
        case d < 7*24*time.Hour:
                day := int(d / (24 * time.Hour))
                return strconv.Itoa(day) + "d ago"
        case d < 30*24*time.Hour:
                w := int(d / (7 * 24 * time.Hour))
                return strconv.Itoa(w) + "w ago"
        case d < 365*24*time.Hour:
                mon := int(d / (30 * 24 * time.Hour))
                return strconv.Itoa(mon) + "mo ago"
        default:
                y := int(d / (365 * 24 * time.Hour))
                return strconv.Itoa(y) + "y ago"
        }
}

// serverError logs the wrapped error and renders a 500 page.
func (a *app) serverError(w http.ResponseWriter, r *http.Request, where string, err error) {
        log.Printf("[error] %s: %v", where, err)
        w.WriteHeader(http.StatusInternalServerError)
        a.render(w, r, "error.html", map[string]any{
                "Title": "Something went wrong",
                "Where": where,
                "Err":   err.Error(),
        })
}
