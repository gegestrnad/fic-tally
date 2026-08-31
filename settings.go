package main

// settings.go — server-side UI preference storage.
//
// Every user-configurable look-and-feel knob (library layout, progress
// ribbon, completion emblem, theme) is persisted in the server's SQLite
// database rather than in per-browser localStorage/cookies. The prefs
// therefore follow the database: opening the app from a different browser
// (or after moving/copying fic-tally.db) shows the same configuration.
//
// Storage shape: one row per preference group in the `settings` table,
// where `value` is the canonical JSON for that group:
//
//   layout | "compact"                         (JSON string)
//   ribbon | {"color":"","opacity":1,...}      (JSON object)
//   emblem | {"show":"on","style":"seal",...}  (JSON object)
//   theme  | "dark"                            (JSON string)
//
// Absent rows mean "use the built-in defaults". Values are validated and
// canonicalized on write (see parseSettingsPatch) so a malformed or hostile
// client payload can never store arbitrary strings — only enum members,
// bounded numbers and #rrggbb colors ever reach the table.
//
// Render flow (app.render): the current settings are read once per page,
// injected as template data — Theme plus the data-* attributes rendered
// directly onto <html> (so layout/shape/corner prefs apply even with
// JavaScript disabled) plus the SettingsJSON blob that the inline pre-paint
// script in layout.html turns into CSS custom properties.
//
// Client flow (static/js/app.js): reads the same blob, applies changes
// immediately for instant feedback, and persists them with a debounced
// POST /api/settings. A one-time migration pushes any legacy localStorage
// prefs from pre-server-settings builds to the server, then deletes them.

import (
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "log"
        "net/http"
        "net/url"
        "strings"
)

// Preference group keys (settings table primary keys).
const (
        SettingLayout  = "layout"
        SettingRibbon  = "ribbon"
        SettingEmblem  = "emblem"
        SettingTheme   = "theme"
        SettingLibrary = "library"
        SettingShelves = "shelves"
)

// MaxShelves caps how many saved views a user can pin. Enough for every
// sensible workflow ("Reading now", "Seasonal cull", "Dropped maybe"),
// small enough that the shelves row can never turn into a wall of chips.
const MaxShelves = 12

// ShelfPrefs is one saved view ("shelf"): a user-chosen name plus the
// canonical query string that reproduces the library view it snapshots
// (any subset of q / status / type / tag / sort). Shelves live in the
// settings table like the other preference groups, so they follow the
// database to every browser.
type ShelfPrefs struct {
        Name   string `json:"name"`   // 1..40 chars, trimmed
        Params string `json:"params"` // canonical "q=…&sort=…" (already encoded)
}

// RibbonPrefs is the canonical progress-ribbon configuration. Field order
// matters for nothing but readability; json tags are the wire/storage
// format and MUST match the legacy localStorage shape so migration is a
// verbatim copy.
type RibbonPrefs struct {
        Color   string  `json:"color"`   // "" = theme crimson
        Opacity float64 `json:"opacity"` // 0.15 .. 1
        Width   int     `json:"width"`   // px, 3 .. 20
        Shape   string  `json:"shape"`   // tag | line | triangle | round
        Side    string  `json:"side"`    // left | right
}

// DefaultRibbonPrefs mirrors the CSS fallbacks (var(--bm-width, 11px) etc.)
// and the client-side BM_DEFAULTS in app.js. Keep the three in sync.
func DefaultRibbonPrefs() RibbonPrefs {
        return RibbonPrefs{Color: "", Opacity: 1, Width: 11, Shape: "tag", Side: "left"}
}

// EmblemPrefs is the canonical completion-emblem configuration.
type EmblemPrefs struct {
        Show    string  `json:"show"`    // on | off
        Style   string  `json:"style"`   // seal | check | star
        Color   string  `json:"color"`   // "" = gold
        Size    int     `json:"size"`    // px, 16 .. 40
        Opacity float64 `json:"opacity"` // 0.15 .. 1
        Pos     string  `json:"pos"`     // tl | tr | bl | br
}

// DefaultEmblemPrefs mirrors the CSS fallbacks (var(--em-size,26px) etc.)
// and EM_DEFAULTS in app.js.
func DefaultEmblemPrefs() EmblemPrefs {
        return EmblemPrefs{Show: "on", Style: "seal", Color: "", Size: 26, Opacity: 1, Pos: "br"}
}

// LibraryPrefs is the canonical library-view configuration. Currently just
// the saved default sort; the group exists so future library defaults
// (status/type filters, page size…) can be added without another storage
// migration.
type LibraryPrefs struct {
        Sort string `json:"sort"` // last_read | title | rating | updated
}

// DefaultLibraryPrefs returns the built-in library defaults. "updated"
// (updated_at desc) is the out-of-the-box sort — most useful default for a
// tracker because it surfaces whatever you touched last, including metadata
// edits that don't set last_read_at.
func DefaultLibraryPrefs() LibraryPrefs {
        return LibraryPrefs{Sort: "updated"}
}

// validSortOption reports whether s is one of the library sort enums. Kept
// here (next to the settings validation) so handleLibrary and
// parseLibraryPrefs share one source of truth for the enum.
func validSortOption(s string) bool {
        switch s {
        case "last_read", "title", "rating", "updated":
                return true
        }
        return false
}

// uiSettings is the joined view of all preference groups. It is also the
// wire format for GET/POST /api/settings and the shape of the SettingsJSON
// blob rendered into every page: absent groups are omitted, and the client
// falls back to its built-in defaults for them.
type uiSettings struct {
        Layout  string        `json:"layout,omitempty"`  // "" = unset (default)
        Ribbon  *RibbonPrefs  `json:"ribbon,omitempty"`  // nil = unset
        Emblem  *EmblemPrefs  `json:"emblem,omitempty"`  // nil = unset
        Theme   string        `json:"theme,omitempty"`   // "" = unset (dark)
        Library *LibraryPrefs `json:"library,omitempty"` // nil = unset
        Shelves []ShelfPrefs  `json:"shelves,omitempty"` // nil = none saved
}

// loadUISettings reads the settings table and defensively re-validates each
// stored blob. A group that fails validation (e.g. hand-edited DB row) is
// logged and skipped rather than breaking page rendering.
func (a *app) loadUISettings() uiSettings {
        var s uiSettings
        kv, err := a.store.Settings()
        if err != nil {
                log.Printf("[error] read settings: %v", err)
                return s
        }
        if raw, ok := kv[SettingLayout]; ok {
                var v string
                if err := json.Unmarshal([]byte(raw), &v); err == nil &&
                        (v == "default" || v == "compact" || v == "details") {
                        s.Layout = v
                } else {
                        log.Printf("[warn] settings: ignoring invalid layout blob %q", raw)
                }
        }
        if raw, ok := kv[SettingRibbon]; ok {
                if p, err := parseRibbonPrefs([]byte(raw)); err == nil {
                        s.Ribbon = &p
                } else {
                        log.Printf("[warn] settings: ignoring invalid ribbon blob: %v", err)
                }
        }
        if raw, ok := kv[SettingEmblem]; ok {
                if p, err := parseEmblemPrefs([]byte(raw)); err == nil {
                        s.Emblem = &p
                } else {
                        log.Printf("[warn] settings: ignoring invalid emblem blob: %v", err)
                }
        }
        if raw, ok := kv[SettingTheme]; ok {
                var v string
                if err := json.Unmarshal([]byte(raw), &v); err == nil && (v == "light" || v == "dark") {
                        s.Theme = v
                } else {
                        log.Printf("[warn] settings: ignoring invalid theme blob %q", raw)
                }
        }
        if raw, ok := kv[SettingLibrary]; ok {
                if p, err := parseLibraryPrefs([]byte(raw)); err == nil {
                        s.Library = &p
                } else {
                        log.Printf("[warn] settings: ignoring invalid library blob: %v", err)
                }
        }
        if raw, ok := kv[SettingShelves]; ok {
                if shelves, err := parseShelvesPrefs([]byte(raw), a.options()); err == nil {
                        s.Shelves = shelves
                } else {
                        log.Printf("[warn] settings: ignoring invalid shelves blob: %v", err)
                }
        }
        return s
}

// settingsJSON marshals s for embedding in a page. json.Marshal HTML-escapes
// <, > and & (\u003c etc.), so the blob can never terminate the surrounding
// <script> element; html/template escapes it again as text on top.
func settingsJSON(s uiSettings) string {
        b, err := json.Marshal(s)
        if err != nil {
                return "{}"
        }
        return string(b)
}

// resolveTheme returns the theme to render with: the stored setting when
// present, otherwise — exactly once — the legacy per-browser theme cookie
// from pre-server-settings builds is adopted as the server-side setting,
// falling back to dark. This gives upgraders their chosen theme on every
// browser without keeping cookie state alive.
func (a *app) resolveTheme(r *http.Request, s uiSettings) string {
        if s.Theme == "light" || s.Theme == "dark" {
                return s.Theme
        }
        if c, err := r.Cookie("theme"); err == nil && (c.Value == "light" || c.Value == "dark") {
                if err := a.store.SaveSettings(map[string]string{SettingTheme: quoteJSON(c.Value)}); err != nil {
                        log.Printf("[error] migrate theme cookie into settings: %v", err)
                } else {
                        log.Printf("settings: adopted legacy theme cookie (%s) as the server-side theme", c.Value)
                }
                return c.Value
        }
        return "dark"
}

// quoteJSON returns v as a canonical JSON string ("dark" → "\"dark\"").
func quoteJSON(v string) string {
        b, err := json.Marshal(v)
        if err != nil {
                return "\"\""
        }
        return string(b)
}

// parseRibbonPrefs validates a ribbon group payload and returns the
// canonical (defaults filled in) value. Fields absent from the payload keep
// their defaults, so partial updates are safe; every present field must
// pass validation or the whole group is rejected.
func parseRibbonPrefs(raw []byte) (RibbonPrefs, error) {
        var patch struct {
                Color   *string  `json:"color"`
                Opacity *float64 `json:"opacity"`
                Width   *int     `json:"width"`
                Shape   *string  `json:"shape"`
                Side    *string  `json:"side"`
        }
        if err := json.Unmarshal(raw, &patch); err != nil {
                return RibbonPrefs{}, errors.New("ribbon must be a JSON object")
        }
        p := DefaultRibbonPrefs()
        if patch.Color != nil {
                if *patch.Color != "" && !isHexColor(*patch.Color) {
                        return RibbonPrefs{}, errors.New("ribbon.color must be \"\" or #rrggbb")
                }
                p.Color = *patch.Color
        }
        if patch.Opacity != nil {
                if *patch.Opacity < 0.15 || *patch.Opacity > 1 {
                        return RibbonPrefs{}, errors.New("ribbon.opacity must be between 0.15 and 1")
                }
                p.Opacity = *patch.Opacity
        }
        if patch.Width != nil {
                if *patch.Width < 3 || *patch.Width > 20 {
                        return RibbonPrefs{}, errors.New("ribbon.width must be between 3 and 20")
                }
                p.Width = *patch.Width
        }
        if patch.Shape != nil {
                switch *patch.Shape {
                case "tag", "line", "triangle", "round":
                        p.Shape = *patch.Shape
                default:
                        return RibbonPrefs{}, errors.New("ribbon.shape must be one of tag|line|triangle|round")
                }
        }
        if patch.Side != nil {
                if *patch.Side != "left" && *patch.Side != "right" {
                        return RibbonPrefs{}, errors.New("ribbon.side must be left or right")
                }
                p.Side = *patch.Side
        }
        return p, nil
}

// parseEmblemPrefs validates an emblem group payload, same contract as
// parseRibbonPrefs.
func parseEmblemPrefs(raw []byte) (EmblemPrefs, error) {
        var patch struct {
                Show    *string  `json:"show"`
                Style   *string  `json:"style"`
                Color   *string  `json:"color"`
                Size    *int     `json:"size"`
                Opacity *float64 `json:"opacity"`
                Pos     *string  `json:"pos"`
        }
        if err := json.Unmarshal(raw, &patch); err != nil {
                return EmblemPrefs{}, errors.New("emblem must be a JSON object")
        }
        p := DefaultEmblemPrefs()
        if patch.Show != nil {
                if *patch.Show != "on" && *patch.Show != "off" {
                        return EmblemPrefs{}, errors.New("emblem.show must be on or off")
                }
                p.Show = *patch.Show
        }
        if patch.Style != nil {
                switch *patch.Style {
                case "seal", "check", "star":
                        p.Style = *patch.Style
                default:
                        return EmblemPrefs{}, errors.New("emblem.style must be one of seal|check|star")
                }
        }
        if patch.Color != nil {
                if *patch.Color != "" && !isHexColor(*patch.Color) {
                        return EmblemPrefs{}, errors.New("emblem.color must be \"\" or #rrggbb")
                }
                p.Color = *patch.Color
        }
        if patch.Size != nil {
                if *patch.Size < 16 || *patch.Size > 40 {
                        return EmblemPrefs{}, errors.New("emblem.size must be between 16 and 40")
                }
                p.Size = *patch.Size
        }
        if patch.Opacity != nil {
                if *patch.Opacity < 0.15 || *patch.Opacity > 1 {
                        return EmblemPrefs{}, errors.New("emblem.opacity must be between 0.15 and 1")
                }
                p.Opacity = *patch.Opacity
        }
        if patch.Pos != nil {
                switch *patch.Pos {
                case "tl", "tr", "bl", "br":
                        p.Pos = *patch.Pos
                default:
                        return EmblemPrefs{}, errors.New("emblem.pos must be one of tl|tr|bl|br")
                }
        }
        return p, nil
}

// parseLibraryPrefs validates a library group payload, same contract as
// parseRibbonPrefs. The only field today is the saved default sort.
func parseLibraryPrefs(raw []byte) (LibraryPrefs, error) {
        var patch struct {
                Sort *string `json:"sort"`
        }
        if err := json.Unmarshal(raw, &patch); err != nil {
                return LibraryPrefs{}, errors.New("library must be a JSON object")
        }
        p := DefaultLibraryPrefs()
        if patch.Sort != nil {
                if !validSortOption(*patch.Sort) {
                        return LibraryPrefs{}, errors.New("library.sort must be one of last_read|title|rating|updated")
                }
                p.Sort = *patch.Sort
        }
        return p, nil
}

// isHexColor reports whether s is a 6-digit #rrggbb hex color. Validation
// happens server-side so nothing but strict enum/color/number values is
// ever stored (the blob is also re-rendered into pages as JSON — see
// settingsJSON — so this is a defense-in-depth requirement, not polish).
func isHexColor(s string) bool {
        if len(s) != 7 || s[0] != '#' {
                return false
        }
        for i := 1; i < 7; i++ {
                c := s[i]
                if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
                        return false
                }
        }
        return true
}

// canonicalShelfParams builds the canonical query string for a shelf from
// raw filter values: only known keys, only valid enum values (checked
// against the CURRENT option lists — a shelf saved before an option was
// renamed still matches, because shelves pin values, not labels), empties
// dropped, keys sorted (url.Values.Encode does that). This is the single
// normalization point for shelves — saving a shelf, comparing the current
// view against a saved one, and re-validating stored blobs all go through
// it, so "isekai,romance" and "isekai, romance" are the same shelf.
func (o optionLists) canonicalShelfParams(q, status, typ, tag, sort string) (string, error) {
        v := url.Values{}
        if q = strings.TrimSpace(q); q != "" {
                if len(q) > 200 {
                        return "", errors.New("search query too long for a shelf")
                }
                v.Set("q", q)
        }
        if status != "" {
                if !o.validStatus(status) {
                        return "", fmt.Errorf("unknown status %q", status)
                }
                v.Set("status", status)
        }
        if typ != "" {
                if !o.validType(typ) {
                        return "", fmt.Errorf("unknown type %q", typ)
                }
                v.Set("type", typ)
        }
        if tag = strings.TrimSpace(tag); tag != "" {
                parts := normalizeTagList(tag)
                if len(parts) == 0 {
                        return "", errors.New("empty tag list")
                }
                if joined := strings.Join(parts, ","); len(joined) > 200 {
                        return "", errors.New("tag list too long for a shelf")
                } else {
                        v.Set("tag", joined)
                }
        }
        if sort != "" {
                if !validSortOption(sort) {
                        return "", fmt.Errorf("unknown sort %q", sort)
                }
                v.Set("sort", sort)
        }
        return v.Encode(), nil
}

// normalizeTagList splits a comma-separated tag list, trims each entry and
// drops empties. Case is preserved (tags are case-insensitive at match time
// but the shelf shows what the user typed).
func normalizeTagList(s string) []string {
        var out []string
        for _, t := range strings.Split(s, ",") {
                if t = strings.TrimSpace(t); t != "" {
                        out = append(out, t)
                }
        }
        return out
}

// parseShelvesPrefs validates a shelves blob (or API payload): a JSON array
// of {name, params}. Each params string must round-trip through the same
// canonicalization the save path uses, so a hand-edited DB row can't smuggle
// arbitrary keys into a shelf link. opts carries the current option lists
// for the enum checks.
func parseShelvesPrefs(raw []byte, opts optionLists) ([]ShelfPrefs, error) {
        var list []ShelfPrefs
        if err := json.Unmarshal(raw, &list); err != nil {
                return nil, errors.New("shelves must be a JSON array of {name, params}")
        }
        if len(list) > MaxShelves {
                return nil, fmt.Errorf("too many shelves (max %d)", MaxShelves)
        }
        out := make([]ShelfPrefs, 0, len(list))
        seen := map[string]bool{}
        for i, sh := range list {
                name := strings.TrimSpace(sh.Name)
                if name == "" || len(name) > 40 {
                        return nil, fmt.Errorf("shelves[%d].name must be 1-40 characters", i)
                }
                if seen[name] {
                        return nil, fmt.Errorf("duplicate shelf name %q", name)
                }
                seen[name] = true
                q, err := url.ParseQuery(sh.Params)
                if err != nil {
                        return nil, fmt.Errorf("shelves[%d].params is not a valid query string", i)
                }
                // Re-canonicalize through the same path that built it; any drift
                // (unknown key, invalid enum, extra junk) is rejected.
                canon, err := opts.canonicalShelfParams(q.Get("q"), q.Get("status"), q.Get("type"), q.Get("tag"), q.Get("sort"))
                if err != nil {
                        return nil, fmt.Errorf("shelves[%d].params: %v", i, err)
                }
                // The canonical form must equal the stored form — Encode() sorts keys
                // and re-escapes values, so a stored string that differs would contain
                // keys we dropped or a non-canonical encoding.
                if canon != sh.Params {
                        return nil, fmt.Errorf("shelves[%d].params is not canonical", i)
                }
                out = append(out, ShelfPrefs{Name: name, Params: canon})
        }
        return out, nil
}

// parseSettingsPatch validates a POST /api/settings body and returns the
// key → canonical-JSON-blob map to upsert. Present groups are replaced
// wholesale (the client always sends complete group objects); groups absent
// from the body are left untouched. Unknown groups are rejected so client
// typos surface immediately instead of writing junk rows. The "options"
// group (dropdown vocabularies) deliberately has no case here — it is
// managed exclusively by POST /options/save with its own stronger
// validation, so it can't be smuggled in through the settings API.
func parseSettingsPatch(body []byte, opts optionLists) (map[string]string, error) {
        var raw map[string]json.RawMessage
        if err := json.Unmarshal(body, &raw); err != nil {
                return nil, fmt.Errorf("invalid JSON: %v", err)
        }
        out := map[string]string{}
        for key, val := range raw {
                switch key {
                case SettingLayout:
                        var v string
                        if err := json.Unmarshal(val, &v); err != nil ||
                                (v != "default" && v != "compact" && v != "details") {
                                return nil, errors.New("layout must be one of default|compact|details")
                        }
                        out[key] = quoteJSON(v)
                case SettingTheme:
                        var v string
                        if err := json.Unmarshal(val, &v); err != nil || (v != "light" && v != "dark") {
                                return nil, errors.New("theme must be light or dark")
                        }
                        out[key] = quoteJSON(v)
                case SettingRibbon:
                        p, err := parseRibbonPrefs(val)
                        if err != nil {
                                return nil, err
                        }
                        b, _ := json.Marshal(p)
                        out[key] = string(b)
                case SettingEmblem:
                        p, err := parseEmblemPrefs(val)
                        if err != nil {
                                return nil, err
                        }
                        b, _ := json.Marshal(p)
                        out[key] = string(b)
                case SettingLibrary:
                        p, err := parseLibraryPrefs(val)
                        if err != nil {
                                return nil, err
                        }
                        b, _ := json.Marshal(p)
                        out[key] = string(b)
                case SettingShelves:
                        shelves, err := parseShelvesPrefs(val, opts)
                        if err != nil {
                                return nil, err
                        }
                        b, _ := json.Marshal(shelves)
                        out[key] = string(b)
                default:
                        return nil, fmt.Errorf("unknown settings group %q", key)
                }
        }
        return out, nil
}

// sameOrigin reports whether the request's Origin header (sent by browsers
// on cross-site POSTs, including fetch no-cors and sendBeacon) matches the
// host the request was served from. Requests without an Origin header
// (curl, same-origin navigations) are allowed. This blocks a malicious web
// page from flipping the user's preferences via a drive-by POST to
// localhost while keeping the API trivial to script for the owner.
func sameOrigin(r *http.Request) bool {
        origin := r.Header.Get("Origin")
        if origin == "" || origin == "null" {
                return true
        }
        u, err := url.Parse(origin)
        if err != nil {
                return false
        }
        return u.Host == r.Host
}

// handleSettingsGet serves GET /api/settings — the current preference
// groups as JSON (absent groups omitted, client applies defaults).
func (a *app) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
        s := a.loadUISettings()
        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(s); err != nil {
                log.Printf("[error] encode settings: %v", err)
        }
}

// handleSettingsPost serves POST /api/settings — validate, upsert, and
// return the resulting settings. The body is parsed as JSON regardless of
// Content-Type: the pagehide flush uses navigator.sendBeacon, which cannot
// send application/json cross-browser (it posts text/plain).
func (a *app) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
        if !sameOrigin(r) {
                http.Error(w, "cross-origin settings writes are not allowed", http.StatusForbidden)
                return
        }
        body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
        if err != nil {
                http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
                return
        }
        updates, err := parseSettingsPatch(body, a.options())
        if err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        if len(updates) > 0 {
                if err := a.store.SaveSettings(updates); err != nil {
                        a.serverError(w, r, "save settings", err)
                        return
                }
        }
        a.handleSettingsGet(w, r)
}

// handleTheme serves POST /theme — the light/dark toggle form. The choice
// is stored as a server-side setting (per-server, not per-browser cookie as
// in earlier builds); a legacy cookie is still adopted on first render via
// resolveTheme so upgraders keep their theme everywhere.
func (a *app) handleTheme(w http.ResponseWriter, r *http.Request) {
        val := r.PostFormValue("theme")
        if val != "light" && val != "dark" {
                val = "dark"
        }
        if err := a.store.SaveSettings(map[string]string{SettingTheme: quoteJSON(val)}); err != nil {
                a.serverError(w, r, "save theme", err)
                return
        }
        back := r.PostFormValue("back")
        if back == "" || !strings.HasPrefix(back, "/") {
                back = "/"
        }
        http.Redirect(w, r, back, http.StatusSeeOther)
}

// --- Saved views ("shelves") -----------------------------------------------
//
// A shelf is a named library view: the current filter+sort combination
// stored as a canonical query string. Saving and deleting are plain form
// POSTs (no JavaScript needed — the <details> popover in library.html is a
// native element), and clicking a shelf chip is just a link to /?<params>.
// Storage goes through the same settings table as the other preference
// groups, so shelves follow the database to every browser.

// shelfView is the template-facing shape for one shelf chip.
type shelfView struct {
        Name   string
        Href   string // "/?q=…" — params already encoded
        Active bool   // current view reproduces this shelf exactly
}

// handleShelfSave serves POST /shelves/save. The form carries the current
// filter values (q/status/type/tag/sort, rendered as hidden inputs by
// library.html) plus the user's chosen name. Saving with an existing name
// replaces that shelf (rename-by-resave); the list is capped at MaxShelves.
func (a *app) handleShelfSave(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse shelf form", err)
                return
        }
        name := strings.TrimSpace(r.PostFormValue("name"))
        if name == "" || len(name) > 40 {
                http.Error(w, "shelf name must be 1-40 characters", http.StatusBadRequest)
                return
        }
        params, err := a.options().canonicalShelfParams(
                r.PostFormValue("q"), r.PostFormValue("status"), r.PostFormValue("type"),
                r.PostFormValue("tag"), r.PostFormValue("sort"))
        if err != nil {
                http.Error(w, "could not save this view: "+err.Error(), http.StatusBadRequest)
                return
        }
        if params == "" {
                http.Error(w, "nothing to save — set a filter or pick a sorting first", http.StatusBadRequest)
                return
        }

        s := a.loadUISettings()
        shelves := s.Shelves
        replaced := false
        for i, sh := range shelves {
                if sh.Name == name {
                        shelves[i].Params = params
                        replaced = true
                        break
                }
        }
        if !replaced {
                if len(shelves) >= MaxShelves {
                        http.Error(w, fmt.Sprintf("too many shelves (max %d) — delete one first", MaxShelves), http.StatusBadRequest)
                        return
                }
                shelves = append(shelves, ShelfPrefs{Name: name, Params: params})
        }
        if err := a.saveShelves(shelves); err != nil {
                a.serverError(w, r, "save shelves", err)
                return
        }
        http.Redirect(w, r, "/"+(map[bool]string{true: "?" + params, false: ""})[params != ""], http.StatusSeeOther)
}

// handleShelfDelete serves POST /shelves/delete — removes the shelf with
// the posted name and bounces back to the (unchanged) current view.
func (a *app) handleShelfDelete(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
                a.serverError(w, r, "parse shelf delete form", err)
                return
        }
        name := strings.TrimSpace(r.PostFormValue("name"))
        s := a.loadUISettings()
        out := make([]ShelfPrefs, 0, len(s.Shelves))
        for _, sh := range s.Shelves {
                if sh.Name != name {
                        out = append(out, sh)
                }
        }
        if len(out) != len(s.Shelves) {
                if err := a.saveShelves(out); err != nil {
                        a.serverError(w, r, "save shelves", err)
                        return
                }
        }
        back := r.PostFormValue("back")
        if back != "" && strings.HasPrefix(back, "?") {
                http.Redirect(w, r, "/"+back, http.StatusSeeOther)
                return
        }
        http.Redirect(w, r, "/", http.StatusSeeOther)
}

// saveShelves persists the full shelf list as the "shelves" settings group.
func (a *app) saveShelves(shelves []ShelfPrefs) error {
        b, err := json.Marshal(shelves)
        if err != nil {
                return err
        }
        return a.store.SaveSettings(map[string]string{SettingShelves: string(b)})
}
