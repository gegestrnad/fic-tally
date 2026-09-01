package main

// mu_handlers.go — the four MangaUpdates lookup routes and the shared
// form-rendering they all funnel into.
//
// Routes (see newServer in app.go):
//
//	POST /series/new/lookup          handleNewLookup        — search, re-render add form
//	POST /series/new/lookup/confirm  handleNewLookupConfirm — fetch + map, re-render add form
//	POST /series/{id}/lookup         handleLookup           — search, re-render edit form
//	POST /series/{id}/lookup/confirm handleLookupConfirm    — fetch + map, re-render edit form
//
// All four are RE-RENDERS, never saves: the lookup flow only pre-fills the
// form (or shows a result list / an inline error), and the user reviews and
// clicks the normal Save button. That is the whole UX contract — a failed
// lookup must never 500 the page or touch stored data, and a confirmed
// lookup must never write Entry fields. The confirm handlers therefore
// rebuild the Series from the STORED row (edit) or a zero value (add),
// apply muApply to that copy, and render — nothing hits the Store.

import (
        "errors"
        "fmt"
        "net/http"
        "strconv"
        "strings"
)

// muFormBase assembles the template data dict every edit.html render
// shares — the same keys handleAddForm / handleEditForm pass, so the MU
// lookup re-renders look exactly like the ordinary form renders.
func (a *app) muFormBase(isNew bool, item EntryWithSeries, seriesID string) map[string]any {
        action := "/series/new"
        title := "Add series"
        exclude := ""
        if !isNew {
                action = "/series/" + seriesID + "/edit"
                title = "Edit: " + item.Title
                exclude = seriesID
        }
        data := map[string]any{
                "Title":          title,
                "FormAction":     action,
                "IsNew":          isNew,
                "Item":           item,
                "AllStatuses":    a.statusOptions(),
                "AllTypes":       a.typeOptions(),
                "AllPubStatuses": a.pubStatusOptions(),
                "AllSeries":      a.seriesOptions(exclude),
                "AllTags":        a.allTags(),
                // MUCurrentID is the MU series id this series already points at (its
                // SourceURL slug, base36 → int; 0 = not linked). The lookup panel
                // marks a hit row "current" when the ids match — zero on the add
                // form, so no row is ever marked there.
                "MUCurrentID": muIDFromSourceURL(item.SourceURL),
        }
        if !isNew {
                data["HasCover"] = item.CoverURL != ""
                data["CoverSrc"] = coverSrc(item.Series)
        }
        return data
}

// muLookupViewModel is the shape the edit.html lookup panel consumes:
// one of Hits (a successful search), Error (a friendly failure), or
// Applied (a confirmed lookup that pre-filled the form).
type muLookupViewModel struct {
        Query   string
        Lookup  string // the lookup action URL (form target), e.g. /series/{id}/lookup
        Confirm string // the confirm action URL, e.g. /series/{id}/lookup/confirm
        Hits    []MUSeriesHit
        Error   string
        Applied string // human summary, e.g. "Filled from Naruto (Manga, 1999)"
}

// muEmptyViewModel is the lookup panel state for a fresh form render (no
// search run yet): just the action URLs, so the search box is live from
// first paint. The ordinary form handlers (handleAddForm / handleEditForm)
// pass this so the panel renders before any lookup POST.
func muEmptyViewModel(lookupAction string) muLookupViewModel {
        return muLookupViewModel{Lookup: lookupAction, Confirm: lookupAction + "/confirm"}
}

// muSearchAndRender runs a MU search for the submitted query and re-renders
// the given form with the result panel (hits or inline error) attached.
// The form fields re-render from the item as-is — a search never mutates
// what the user already typed or what the DB holds.
func (a *app) muSearchAndRender(w http.ResponseWriter, r *http.Request, isNew bool, item EntryWithSeries, seriesID, lookupAction string) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse lookup form", err)
                return
        }
        query := strings.TrimSpace(r.PostForm.Get("query"))
        vm := muLookupViewModel{Query: query, Lookup: lookupAction, Confirm: lookupAction + "/confirm"}
        if query == "" {
                vm.Error = "Enter a title to search."
        } else {
                hits, err := a.mu.muSearch(query, 10)
                if err != nil {
                        vm.Error = fmt.Sprintf("MangaUpdates lookup failed: %v", err)
                } else if len(hits) == 0 {
                        vm.Error = fmt.Sprintf("No MangaUpdates results for %q.", query)
                } else {
                        vm.Hits = hits
                }
        }
        data := a.muFormBase(isNew, item, seriesID)
        data["MU"] = vm
        a.render(w, r, "edit.html", data)
}

// muConfirmAndRender fetches the full record for the chosen series id,
// applies the field mapping to a COPY of the series, and re-renders the
// form pre-filled. Nothing is saved: the user reviews and clicks Save.
//
// The existing series (or empty one, for the add form) is the base — so
// for an edit, every field the mapping does not override (e.g. ParentID,
// an ongoing series' total chapters) survives exactly as stored.
func (a *app) muConfirmAndRender(w http.ResponseWriter, r *http.Request, isNew bool, item EntryWithSeries, seriesID, lookupAction string) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse lookup confirm form", err)
                return
        }
        // series_id arrives as a decimal string (from the hidden form field).
        // It comes from a hit row the server itself rendered, so a bad value
        // can only mean a tampered/duplicate submission — treat it as "no
        // series selected" and show the panel error, never 500.
        rawID := strings.TrimSpace(r.PostForm.Get("series_id"))
        id, err := strconv.ParseInt(rawID, 10, 64)
        if err != nil || id <= 0 {
                id = 0
        }
        vm := muLookupViewModel{Query: item.Title, Lookup: lookupAction, Confirm: lookupAction + "/confirm"}
        data := a.muFormBase(isNew, item, seriesID)

        applyAndRender := func(rec *muSeriesRecord) {
                // Map onto a copy of the series: Entry fields are not part of this
                // struct, and the Series copy keeps ID/ParentID/CreatedAt intact.
                ser := item.Series
                muApply(&ser, rec, a.options())
                vm.Applied = muAppliedSummary(rec)
                data = a.muFormBase(isNew, mergeSeriesInto(item, ser), seriesID)
                data["MU"] = vm
                a.render(w, r, "edit.html", data)
        }

        if id <= 0 {
                vm.Error = "No series selected."
                data["MU"] = vm
                a.render(w, r, "edit.html", data)
                return
        }
        rec, err := a.mu.muFetch(id)
        if err != nil {
                vm.Error = fmt.Sprintf("Could not fetch the MangaUpdates record: %v", err)
                data["MU"] = vm
                a.render(w, r, "edit.html", data)
                return
        }
        applyAndRender(rec)
}

// mergeSeriesInto keeps the Entry half of the joined row and swaps in the
// mapped Series — the shape the form renders (EntryWithSeries).
func mergeSeriesInto(item EntryWithSeries, ser Series) EntryWithSeries {
        out := item
        out.Series = ser
        return out
}

// muAppliedSummary renders the confirmation line, e.g.
// "Filled from "Naruto" (Manga, 1999). Review the fields, then click Save."
func muAppliedSummary(rec *muSeriesRecord) string {
        s := fmt.Sprintf("Filled from %q", rec.Title)
        if t := strings.TrimSpace(rec.Type); t != "" {
                s += " (" + t
                if y := muYear(rec); y > 0 {
                        s += fmt.Sprintf(", %d", y)
                }
                s += ")"
        }
        return s + ". Review the fields, then click Save."
}

// --- the four routes --------------------------------------------------------

// handleNewLookup: POST /series/new/lookup — search on the ADD form. The
// form re-renders empty (nothing typed is at risk yet, except the search
// box itself, which keeps its query via the view model).
func (a *app) handleNewLookup(w http.ResponseWriter, r *http.Request) {
        a.muSearchAndRender(w, r, true, EntryWithSeries{}, "", "/series/new/lookup")
}

// handleNewLookupConfirm: POST /series/new/lookup/confirm — fetch + map on
// the ADD form. The pre-filled row starts from the SUBMITTED form values
// (rebuildFormViewModel, the same pattern the duplicate-warning re-render
// uses) so anything the user already typed in the add form survives the
// round-trip: the mapping overrides the bibliographic fields it maps, and
// leaves the rest of the user's draft alone.
func (a *app) handleNewLookupConfirm(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse mu confirm form", err)
                return
        }
        item := rebuildFormViewModel(r.PostForm)
        a.muConfirmAndRender(w, r, true, item, "", "/series/new/lookup")
}

// handleLookup: POST /series/{id}/lookup — search on the EDIT form.
func (a *app) handleLookup(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get series for lookup", err)
                return
        }
        a.muSearchAndRender(w, r, false, *item, id, "/series/"+id+"/lookup")
}

// handleLookupConfirm: POST /series/{id}/lookup/confirm — fetch + map on
// the EDIT form, re-rendering with the stored row as the base.
func (a *app) handleLookupConfirm(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        item, err := a.store.Get(id)
        if err != nil {
                if errors.Is(err, ErrNotFound) {
                        http.NotFound(w, r)
                        return
                }
                a.serverError(w, r, "get series for lookup confirm", err)
                return
        }
        a.muConfirmAndRender(w, r, false, *item, id, "/series/"+id+"/lookup")
}
