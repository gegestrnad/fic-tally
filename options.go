package main

// options.go — user-editable dropdown vocabularies.
//
// The reading-status, type and publication-status dropdowns used to be
// hardcoded Go slices (models.go). They are now option lists stored in the
// settings table (group "options") and editable on GET/POST /options, so
// renaming "Completed" to "Complete", dropping an unused value or adding a
// custom type ("Webtoon") is a 10-second self-service form POST instead of a
// code change.
//
// Each option is a {value, label} pair, and the SPLIT IS THE POINT:
//
//   value  the canonical, immutable identifier — what the database stores,
//          what URLs and CSV/JSON import/export use, what shelves pin. It is
//          assigned once (built-ins by the code, custom options as the
//          lowercased label) and NEVER changes afterwards.
//   label  the display text — freely renamable. Renaming a label touches no
//          data: every series, filter, shelf and import keeps working because
//          they all reference the value.
//
// This is why the v8 label change ("Completed" → "Complete" etc.) needs no
// data migration for existing rows — the stored values "completed"/"hiatus"/
// "cancelled" are untouched; only the rendered text changes. The one value
// that genuinely disappears is pub_status "upcoming": its rows are cleared to
// "" (unknown) once, at the same time the options group is seeded (see
// initOptions — the migration runs exactly when the group is absent, i.e. on
// the first v8 start of an old database, never again).
//
// Two safety rails keep the dynamic lists from breaking the app's logic:
//
//   1. Protected values. The five built-in reading statuses power the stats
//      tiles, the completed_at transition (status "completed"), the
//      new-series default ("plan to read") and the fully-completed emblem;
//      pub_status "completed" powers the same emblem. These can be RENAMED
//      (label only) but never removed. Everything else may be removed —
//      but only while no series uses it (checked live against the tables at
//      save time), so no row can end up holding an orphaned value.
//   2. Validation everywhere reads the same live list. Form parsing, import
//      (CSV/JSON), the batch API, bulk status, shelf canonicalization and
//      stats breakdowns all go through a.options(), so a value is either
//      valid everywhere or nowhere.

import (
        "encoding/json"
        "fmt"
        "log"
        "net/http"
        "sort"
        "strconv"
        "strings"
)

// SettingOptions is the settings-table key for the option-list group.
const SettingOptions = "options"

// MaxOptionsPerList caps each dropdown so the selects and the options page
// stay usable (and form-field enumeration stays bounded).
const MaxOptionsPerList = 20

// maxOptionLabel caps label (and derived value) length.
const maxOptionLabel = 40

// option is one dropdown entry. Field names double as the template shape:
// handlers pass []option straight into the "AllStatuses"/"AllTypes"/
// "AllPubStatuses" template keys, where existing selects read .Value/.Label.
type option struct {
        Value string `json:"value"` // canonical ID — stored in the DB, URLs, import
        Label string `json:"label"` // display text — freely renamable
}

// optionLists is the whole "options" settings group.
type optionLists struct {
        Status    []option `json:"status"`
        Type      []option `json:"type"`
        PubStatus []option `json:"pub_status"`
}

// defaultOptionLists returns the built-in vocabularies. Labels reflect the
// v8 naming the user asked for: Ongoing | Complete | Hiatus | Canceled with
// no "Upcoming". Values keep the original canonical spellings.
func defaultOptionLists() optionLists {
        return optionLists{
                Status: []option{
                        {Value: string(StatusReading), Label: "Reading"},
                        {Value: string(StatusPlanToRead), Label: "Plan to read"},
                        {Value: string(StatusOnHold), Label: "On hold"},
                        {Value: string(StatusDropped), Label: "Dropped"},
                        {Value: string(StatusCompleted), Label: "Completed"},
                },
                Type: []option{
                        {Value: string(TypeManga), Label: "Manga"},
                        {Value: string(TypeManhwa), Label: "Manhwa"},
                        {Value: string(TypeManhua), Label: "Manhua"},
                        {Value: string(TypeLightNovel), Label: "Light novel"},
                        {Value: string(TypeWebNovel), Label: "Web novel"},
                },
                PubStatus: []option{
                        {Value: string(PubOngoing), Label: "Ongoing"},
                        {Value: string(PubCompleted), Label: "Complete"},
                        {Value: string(PubHiatus), Label: "Hiatus"},
                        {Value: string(PubCancelled), Label: "Canceled"},
                },
        }
}

// parseOptionLists decodes and sanity-checks a stored blob. A blob that
// fails any check is treated as absent by initOptions (defaults are used) —
// a hand-corrupted row must never take the validation machinery down with
// it, because every form parse in the app depends on these lists.
func parseOptionLists(raw []byte) (optionLists, error) {
        var o optionLists
        if err := json.Unmarshal(raw, &o); err != nil {
                return o, err
        }
        for _, list := range []struct {
                name     string
                opts     []option
                required []string // semantic anchors that must survive any edit
        }{
                {"status", o.Status, []string{string(StatusReading), string(StatusPlanToRead), string(StatusOnHold), string(StatusDropped), string(StatusCompleted)}},
                {"type", o.Type, nil},
                {"pub_status", o.PubStatus, []string{string(PubCompleted)}},
        } {
                if len(list.opts) == 0 {
                        return o, fmt.Errorf("%s list is empty", list.name)
                }
                if len(list.opts) > MaxOptionsPerList {
                        return o, fmt.Errorf("%s list exceeds %d entries", list.name, MaxOptionsPerList)
                }
                seen := map[string]bool{}
                for _, op := range list.opts {
                        if strings.TrimSpace(op.Value) == "" || strings.TrimSpace(op.Label) == "" {
                                return o, fmt.Errorf("%s list has an empty value or label", list.name)
                        }
                        if seen[op.Value] {
                                return o, fmt.Errorf("%s list has duplicate value %q", list.name, op.Value)
                        }
                        seen[op.Value] = true
                }
                // The semantic anchors must survive any edit — otherwise completed_at
                // transitions, the new-series default, the stats tiles and the
                // completion emblem silently stop matching anything. (Removal is also
                // blocked in the UI; this guards hand-edited blobs.)
                for _, v := range list.required {
                        if !seen[v] {
                                return o, fmt.Errorf("%s list is missing built-in %q", list.name, v)
                        }
                }
        }
        return o, nil
}

// --- lookup helpers ----------------------------------------------------------

// validStatus reports whether s is a current reading-status value. The empty
// string (no filter / not set) is always valid.
func (o optionLists) validStatus(s string) bool {
        if s == "" {
                return true
        }
        for _, op := range o.Status {
                if op.Value == s {
                        return true
                }
        }
        return false
}

// validType reports whether s is a current type value.
func (o optionLists) validType(s string) bool {
        if s == "" {
                return true
        }
        for _, op := range o.Type {
                if op.Value == s {
                        return true
                }
        }
        return false
}

// validPubStatus reports whether s is a current publication-status value.
func (o optionLists) validPubStatus(s string) bool {
        if s == "" {
                return true
        }
        for _, op := range o.PubStatus {
                if op.Value == s {
                        return true
                }
        }
        return false
}

// defaultType is the type applied when a form or import row omits it: the
// historical default ("manga") while it's still in the list, otherwise the
// first option — so removing the default type from the vocabulary can't
// turn every type-less import row into an error.
func (o optionLists) defaultType() SeriesType {
        if o.validType(string(TypeManga)) {
                return TypeManga
        }
        if len(o.Type) > 0 {
                return SeriesType(o.Type[0].Value)
        }
        return TypeManga
}

// statusLabelOf returns the display label for a status value. Unknown
// (orphaned) values fall back to the raw value so a row can never render
// blank — this can only happen for data written before the option existed.
func (o optionLists) statusLabelOf(v string) string {
        for _, op := range o.Status {
                if op.Value == v {
                        return op.Label
                }
        }
        return v
}

// typeLabelOf returns the display label for a type value.
func (o optionLists) typeLabelOf(v string) string {
        if v == "" {
                return ""
        }
        for _, op := range o.Type {
                if op.Value == v {
                        return op.Label
                }
        }
        return v
}

// pubStatusLabelOf returns the display label for a publication status.
// Empty string means "not set" and renders as nothing by design.
func (o optionLists) pubStatusLabelOf(v string) string {
        if v == "" {
                return ""
        }
        for _, op := range o.PubStatus {
                if op.Value == v {
                        return op.Label
                }
        }
        return v
}

// statusValues / typeValues / pubStatusValues join the canonical values —
// used to build helpful validation error messages.
func (o optionLists) statusValues() string {
        vals := make([]string, 0, len(o.Status))
        for _, op := range o.Status {
                vals = append(vals, op.Value)
        }
        return strings.Join(vals, ", ")
}

func (o optionLists) typeValues() string {
        vals := make([]string, 0, len(o.Type))
        for _, op := range o.Type {
                vals = append(vals, op.Value)
        }
        return strings.Join(vals, ", ")
}

func (o optionLists) pubStatusValues() string {
        vals := make([]string, 0, len(o.PubStatus))
        for _, op := range o.PubStatus {
                vals = append(vals, op.Value)
        }
        return strings.Join(vals, ", ")
}

// --- app plumbing -------------------------------------------------------------

// options returns the current option lists (copy of the slice headers; the
// lists themselves are treated as immutable and replaced wholesale on save).
func (a *app) options() optionLists {
        a.optsMu.RLock()
        defer a.optsMu.RUnlock()
        return a.opts
}

// setOptions replaces the in-memory lists.
func (a *app) setOptions(o optionLists) {
        a.optsMu.Lock()
        a.opts = o
        a.optsMu.Unlock()
}

// initOptions loads the option lists at startup: stored group if present and
// valid, otherwise (first run, or a corrupt/hand-edited blob) the defaults —
// persisted so later runs treat the lists as user-managed. The absent-group
// path is also the one-time migration point: pub_status "upcoming" no longer
// exists as an option, so any pre-v8 rows holding it are cleared to "" here.
func (a *app) initOptions() {
        kv, err := a.store.Settings()
        if err != nil {
                log.Printf("[warn] options: cannot read settings (%v) — using built-in defaults", err)
                a.setOptions(defaultOptionLists())
                return
        }
        if raw, ok := kv[SettingOptions]; ok {
                o, err := parseOptionLists([]byte(raw))
                if err == nil {
                        a.setOptions(o)
                        return
                }
                log.Printf("[warn] options: ignoring invalid stored blob (%v) — resetting to defaults", err)
        }
        o := defaultOptionLists()
        a.setOptions(o)

        // One-time data migration: "upcoming" is gone from the vocabulary.
        // Cleared BEFORE the group is persisted: if the process dies between the
        // two writes, the next start sees no options group, repeats the (idempotent)
        // UPDATE — no row can be stranded with an orphaned value.
        if err := a.store.ClearPubStatusValue("upcoming"); err != nil {
                log.Printf("[warn] options: clearing legacy pub_status 'upcoming': %v", err)
        }
        if err := a.saveOptionLists(o); err != nil {
                log.Printf("[warn] options: seeding defaults: %v", err)
        }
}

// saveOptionLists persists the lists as the "options" settings group and
// swaps the in-memory copy.
func (a *app) saveOptionLists(o optionLists) error {
        b, err := json.Marshal(o)
        if err != nil {
                return err
        }
        if err := a.store.SaveSettings(map[string]string{SettingOptions: string(b)}); err != nil {
                return err
        }
        a.setOptions(o)
        return nil
}

// statusOptions / typeOptions / pubStatusOptions feed the template selects
// ("AllStatuses" / "AllTypes" / "AllPubStatuses").
func (a *app) statusOptions() []option    { return a.options().Status }
func (a *app) typeOptions() []option      { return a.options().Type }
func (a *app) pubStatusOptions() []option { return a.options().PubStatus }

// --- protection rules ----------------------------------------------------------

// protectedStatus reports whether a reading-status value is built-in and
// load-bearing: the stats tiles count all five, "completed" drives the
// completed_at transition and the completion emblem, "reading" drives the
// Currently-Reading tile, and "plan to read" is the new-series/import
// default. Removing any of them would silently break that logic, so the
// option rows are locked (labels stay renamable).
func protectedStatus(v string) bool {
        switch v {
        case string(StatusReading), string(StatusPlanToRead), string(StatusOnHold),
                string(StatusDropped), string(StatusCompleted):
                return true
        }
        return false
}

// protectedPubStatus: pub_status "completed" drives the fully-completed
// emblem (reading AND publication both finished). The other publication
// statuses are pure display and may be removed once unused.
func protectedPubStatus(v string) bool {
        return v == string(PubCompleted)
}

// --- GET /options ----------------------------------------------------------------

// optRowView is one editable option row on the options page.
type optRowView struct {
        Field     string // "status" | "type" | "pub_status"
        Value     string
        Label     string
        Pos       int  // 1-based display order
        Protected bool // built-in: label editable, removal locked
        InUse     int  // how many series currently use this value
}

// optSectionView is one dropdown's editor block.
type optSectionView struct {
        Title      string
        Hint       string
        Field      string
        AddExample string
        Rows       []optRowView
}

// handleOptionsForm renders the dropdown-options editor.
func (a *app) handleOptionsForm(w http.ResponseWriter, r *http.Request) {
        o := a.options()

        // Usage counts come from three GROUP BY queries — they power the
        // "N in use" hints and pre-disable the remove checkbox for options
        // that are still referenced (the save handler re-checks live).
        statusUse, err1 := a.store.StatusUsage()
        typeUse, err2 := a.store.TypeUsage()
        pubUse, err3 := a.store.PubStatusUsage()
        if err := firstErr(err1, err2, err3); err != nil {
                a.serverError(w, r, "option usage counts", err)
                return
        }

        build := func(field, title, hint, addExample string, opts []option, protected func(string) bool, use map[string]int) optSectionView {
                sec := optSectionView{Title: title, Hint: hint, Field: field, AddExample: addExample}
                for i, op := range opts {
                        sec.Rows = append(sec.Rows, optRowView{
                                Field:     field,
                                Value:     op.Value,
                                Label:     op.Label,
                                Pos:       i + 1,
                                Protected: protected(op.Value),
                                InUse:     use[op.Value],
                        })
                }
                return sec
        }

        sections := []optSectionView{
                build("status", "Reading status",
                        "Your relationship to a series — the colored dots on cards, the library filter and the stats tiles. The five built-ins are locked (the stats page and completion tracking rely on them) but their labels can be renamed.",
                        "Re-reading", o.Status, protectedStatus, statusUse),
                build("type", "Type",
                        "What kind of work it is. Shown on cards, the detail page and the Type filter.",
                        "Webtoon", o.Type, func(string) bool { return false }, typeUse),
                build("pub_status", "Publication status",
                        "Whether the work itself is still being published — separate from your reading status. \"Complete\" is locked (it drives the fully-completed emblem on covers).",
                        "Oneshot", o.PubStatus, protectedPubStatus, pubUse),
        }

        a.render(w, r, "options.html", map[string]any{
                "Title":    "Dropdown options",
                "Sections": sections,
                "Saved":    r.URL.Query().Get("saved") == "1",
        })
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
        for _, e := range errs {
                if e != nil {
                        return e
                }
        }
        return nil
}

// --- POST /options/save -------------------------------------------------------------

// handleOptionsSave applies renames, removals, additions and reordering in
// one POST. On any problem the whole save is rejected with a 400 and a
// plain-language message — option lists are load-bearing, so a partial
// apply would be worse than a retry.
func (a *app) handleOptionsSave(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse options form", err)
                return
        }
        cur := a.options()

        // Usage counts are fetched up front: removal checks consult them, and
        // fetching per list (not per option) keeps this at three queries.
        statusUse, err1 := a.store.StatusUsage()
        typeUse, err2 := a.store.TypeUsage()
        pubUse, err3 := a.store.PubStatusUsage()
        if err := firstErr(err1, err2, err3); err != nil {
                a.serverError(w, r, "option usage counts", err)
                return
        }

        noProtection := func(string) bool { return false }
        var next optionLists
        fields := []struct {
                key       string
                cur       []option
                dst       *[]option
                protected func(string) bool
                usage     map[string]int
        }{
                {key: "status", cur: cur.Status, dst: &next.Status, protected: protectedStatus, usage: statusUse},
                {key: "type", cur: cur.Type, dst: &next.Type, protected: noProtection, usage: typeUse},
                {key: "pub_status", cur: cur.PubStatus, dst: &next.PubStatus, protected: protectedPubStatus, usage: pubUse},
        }

        for _, f := range fields {
                out, errMsg := applyOptionField(r, optField{
                        key:       f.key,
                        cur:       f.cur,
                        protected: f.protected,
                        usageNoun: "series",
                }, f.usage)
                if errMsg != "" {
                        http.Error(w, errMsg, http.StatusBadRequest)
                        return
                }
                *f.dst = out
        }

        if _, err := parseOptionLists(mustJSON(next)); err != nil {
                // Defensive: the per-field checks above should make this
                // unreachable, but never persist a malformed blob.
                http.Error(w, "refusing to save: "+err.Error(), http.StatusBadRequest)
                return
        }
        if err := a.saveOptionLists(next); err != nil {
                a.serverError(w, r, "save options", err)
                return
        }
        http.Redirect(w, r, "/options?saved=1", http.StatusSeeOther)
}

// mustJSON marshals v, returning "null" only on the impossible failure path.
func mustJSON(v any) []byte {
        b, err := json.Marshal(v)
        if err != nil {
                return []byte("null")
        }
        return b
}

// optField bundles what applyOptionField needs to know about one list.
type optField struct {
        key       string // form prefix + list name: "status" | "type" | "pub_status"
        cur       []option
        protected func(string) bool
        usageNoun string // for error messages
}

// applyOptionField processes one list and returns the new slice, or an
// error message suitable for a 400.
func applyOptionField(r *http.Request, f optField, usage map[string]int) ([]option, string) {
        type staged struct {
                opt option
                pos int
        }
        out := make([]staged, 0, len(f.cur))
        for i, op := range f.cur {
                if r.PostFormValue("del_"+f.key+"_"+op.Value) == "1" {
                        if f.protected(op.Value) {
                                return nil, fmt.Sprintf("%q is a built-in option the app relies on — its label can be renamed, but it cannot be removed", op.Label)
                        }
                        if n := usage[op.Value]; n > 0 {
                                return nil, fmt.Sprintf("%q is still used by %d %s — reassign %s first (e.g. via the bulk status tool), then remove it", op.Label, n, f.usageNoun, map[bool]string{true: "it", false: "them"}[n == 1])
                        }
                        continue
                }
                label := op.Label
                if vals, ok := r.PostForm["label_"+f.key+"_"+op.Value]; ok {
                        label = strings.TrimSpace(vals[0])
                        if label == "" {
                                return nil, fmt.Sprintf("the label for %q must not be empty", op.Value)
                        }
                        if len(label) > maxOptionLabel {
                                return nil, fmt.Sprintf("the label for %q is too long (max %d characters)", op.Value, maxOptionLabel)
                        }
                }
                pos := i + 1
                if vals, ok := r.PostForm["pos_"+f.key+"_"+op.Value]; ok && strings.TrimSpace(vals[0]) != "" {
                        if p, err := strconv.Atoi(strings.TrimSpace(vals[0])); err == nil && p >= 1 && p <= 99 {
                                pos = p
                        }
                }
                out = append(out, staged{opt: option{Value: op.Value, Label: label}, pos: pos})
        }

        // Additions: one per list per save. The value is the lowercased label
        // and is permanent — it becomes the import ID and the stored data key.
        if add := strings.TrimSpace(r.PostFormValue("add_" + f.key)); add != "" {
                if len(add) > maxOptionLabel {
                        return nil, fmt.Sprintf("the new %s option is too long (max %d characters)", f.key, maxOptionLabel)
                }
                value := strings.ToLower(add)
                if value == "" {
                        return nil, fmt.Sprintf("the new %s option must contain letters or digits", f.key)
                }
                if len(out) >= MaxOptionsPerList {
                        return nil, fmt.Sprintf("too many %s options (max %d)", f.key, MaxOptionsPerList)
                }
                for _, s := range out {
                        if s.opt.Value == value || strings.EqualFold(s.opt.Label, add) {
                                return nil, fmt.Sprintf("%q already exists in this list", add)
                        }
                }
                out = append(out, staged{opt: option{Value: value, Label: add}, pos: len(out) + 1})
        }

        if len(out) == 0 {
                return nil, "keep at least one option in the list"
        }

        // Duplicate labels within the final list (case-insensitive) would make
        // the dropdowns ambiguous.
        seen := map[string]bool{}
        for _, s := range out {
                k := strings.ToLower(s.opt.Label)
                if seen[k] {
                        return nil, fmt.Sprintf("duplicate label %q — labels must be unique within a list", s.opt.Label)
                }
                seen[k] = true
        }

        sort.SliceStable(out, func(i, j int) bool { return out[i].pos < out[j].pos })
        final := make([]option, 0, len(out))
        for _, s := range out {
                final = append(final, s.opt)
        }
        return final, ""
}
