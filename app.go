package main

// app.go — application wiring: template loading, route registration, main.

import (
        "encoding/json"
        "flag"
        "html/template"
        "log"
        "net/http"
        "os"
        "path/filepath"
        "strconv"
        "strings"
        "sync"
        "time"
        "unicode"
        "unicode/utf8"
)

// app holds the singletons the handlers need: the Store, the parsed templates,
// the cover-image directory (so the upload handler can write to it), and the
// user-editable dropdown option lists (options.go) cached in memory and
// swapped wholesale on save.
type app struct {
        store    Store
        tpl      *template.Template
        coverDir string // absolute path to static/covers/

        optsMu sync.RWMutex
        opts   optionLists // reading-status / type / publication-status options
}

// render wraps the template execution with the base layout. The layout
// template defines {{block "content" .}}...{{end}}; each page template
// overrides the "content" block. We pass the same data dict to both.
//
// render also injects the server-side UI preferences (layout, ribbon,
// emblem, theme — see settings.go) into every page: the SettingsJSON blob
// consumed by the pre-paint script, the data-* attributes rendered onto
// <html> (so prefs apply even with JavaScript disabled), and Theme.
// Handlers therefore no longer pass "Theme" themselves, and new pages get
// preference support for free.
func (a *app) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
        s := a.loadUISettings()
        if data == nil {
                data = map[string]any{}
        }
        // template.JS: the blob is inserted verbatim into the
        // <script type="application/json"> element in layout.html (html/template
        // would otherwise escape it as a JS string literal, double-encoding it).
        // This is safe by construction: settingsJSON output contains only
        // json.Marshal'd, server-validated preference values (enums, #rrggbb
        // colors, bounded numbers — no quotes, angle brackets or operators),
        // and json.Marshal HTML-escapes <, > and & on top.
        data["SettingsJSON"] = template.JS(settingsJSON(s))
        data["Theme"] = a.resolveTheme(r, s)
        if s.Layout == "compact" || s.Layout == "details" {
                data["LayoutAttr"] = s.Layout
        }
        if s.Ribbon != nil {
                data["RibbonShapeAttr"] = s.Ribbon.Shape
                data["RibbonSideAttr"] = s.Ribbon.Side
        }
        if s.Emblem != nil {
                data["EmblemStyleAttr"] = s.Emblem.Style
                data["EmblemPosAttr"] = s.Emblem.Pos
                if s.Emblem.Show == "off" {
                        data["EmblemHiddenAttr"] = "1"
                }
        }

        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        // Clone so concurrent executes don't race on the template's internal
        // state. Clone is cheap; this is the standard pattern for serving
        // html/template across goroutines.
        tpl, err := a.tpl.Clone()
        if err != nil {
                http.Error(w, "template clone failed", http.StatusInternalServerError)
                return
        }
        if err := tpl.ExecuteTemplate(w, name, data); err != nil {
                // ExecuteTemplate already wrote partial bytes to w; we can only
                // log here, not recover the response.
                log.Printf("[error] render %s: %v", name, err)
        }
}

// slugify converts a title to a URL-safe, filesystem-safe ID. Used when
// creating a new series. Keeps lowercase alphanumerics and dashes; everything
// else collapses to a single dash. Returns "series" if input is empty after
// stripping (caller is expected to have validated title != "" before).
func slugify(s string) string {
        s = strings.ToLower(strings.TrimSpace(s))
        if s == "" {
                return ""
        }
        var b strings.Builder
        prevDash := false
        for _, r := range s {
                switch {
                case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
                        b.WriteRune(r)
                        prevDash = false
                default:
                        if !prevDash {
                                b.WriteRune('-')
                                prevDash = true
                        }
                }
        }
        out := b.String()
        out = strings.Trim(out, "-")
        if out == "" {
                return ""
        }
        return out
}

// loadTemplates parses every *.html under tplDir into one *template.Template.
// The layout file is the entry point; page files define the "content" block.
// Funcs give templates access to a few view-model helpers without polluting
// the handler layer — including the option-label lookups, which close over
// the app so renames apply on the next render without a template reload.
func loadTemplates(a *app, tplDir string) (*template.Template, error) {
        funcs := template.FuncMap{
                "lower":      strings.ToLower,
                "hasPrefix":  strings.HasPrefix,
                "trimSuffix": strings.TrimSuffix,
                // firstChar returns the first rune of s, uppercased, used as the
                // placeholder glyph on a cover when no cover image is set.
                "firstChar": func(s string) string {
                        if s == "" {
                                return "?"
                        }
                        r, _ := utf8.DecodeRuneInString(s)
                        return string(unicode.ToUpper(r))
                },
                // statusDotClass returns the CSS class suffix for a status, used as
                // `class="dot dot-<status>"` so the dot color is set by CSS rules
                // rather than an inline var() — html/template's CSS-in-style
                // escaping rejects inline var() interpolation, and using classes
                // is also cleaner. The implementation lives in stats.go and is
                // shared with the stats breakdown rows.
                "statusDotClass": statusDotClass,
                // NOTE: We previously had selectedAttr and checkedAttr
                // helpers that returned "selected"/"checked"/"". They were
                // removed because html/template substitutes the sentinel
                // `ZgotmplZ` for an empty-string action placed in the
                // "attribute name" position (right after another attribute).
                // Templates now inline `{{if eq ...}} selected{{end}}` and
                // `{{if .X}} checked{{end}}` instead.

                // intStr returns the int value or "" if nil. Used for *int fields
                // like Rating where the form needs a plain text value (or empty
                // so the placeholder shows).
                "intStr": func(p *int) string {
                        if p == nil {
                                return ""
                        }
                        return strconv.Itoa(*p)
                },
                // floatStr returns the float formatted like the chapter-number
                // display (no trailing .0) or "" if nil.
                "floatStr": func(p *float64) string {
                        if p == nil {
                                return ""
                        }
                        return formatChapterNumber(*p)
                },
                // joinStr joins a slice of strings with sep.
                "joinStr": func(ss []string, sep string) string {
                        return strings.Join(ss, sep)
                },
                // containsStr reports whether ss contains s, case-insensitive.
                // Used to highlight the mini-tags on library cards that are
                // part of the active tag filter.
                "containsStr": func(ss []string, s string) bool {
                        for _, v := range ss {
                                if strings.EqualFold(v, s) {
                                        return true
                                }
                        }
                        return false
                },
                // jsonTags marshals a []string to a JSON array, used to hand
                // the list of existing tags to the edit form for autocomplete
                // (data-tags="…"). Returned as a plain string so html/template
                // applies its attribute-context escaping (quotes etc.); the
                // browser decodes entities before JS reads el.dataset.tags.
                "jsonTags": func(ss []string) string {
                        b, err := json.Marshal(ss)
                        if err != nil {
                                return "[]"
                        }
                        return string(b)
                },
                // statusLabel / pubStatusLabel / typeLabel render the CURRENT
                // user-editable label for a stored value (see options.go).
                // They close over the app so every page reflects option
                // renames immediately, without re-parsing templates. Unknown
                // values (data predating a removed option) fall back to the
                // raw value so a card can never render blank.
                "statusLabel": func(s Status) string {
                        return a.options().statusLabelOf(string(s))
                },
                "pubStatusLabel": func(p PubStatus) string {
                        return a.options().pubStatusLabelOf(string(p))
                },
                "typeLabel": func(t SeriesType) string {
                        return a.options().typeLabelOf(string(t))
                },
        }
        glob := filepath.Join(tplDir, "*.html")
        return template.New("").Funcs(funcs).ParseGlob(glob)
}

// noCache wraps a handler so every response carries Cache-Control: no-cache.
// The browser may keep a cached copy but MUST revalidate (If-Modified-Since)
// before reusing it — stale app.js/app.css after an upgrade made the UI look
// broken (new HTML + old assets: unstyled card text, dead buttons, live
// search). Go's http.FileServer sends only a Last-Modified header, and with
// no Cache-Control at all browsers fall back to heuristic caching and may
// serve days-old assets without ever revalidating. no-cache keeps revalidation
// cheap: unchanged files come back as 304s.
func noCache(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Cache-Control", "no-cache")
                next.ServeHTTP(w, r)
        })
}

// newServer wires the routes onto a *http.ServeMux and returns the server.
// Go 1.22's enhanced mux supports method-qualified patterns + path wildcards,
// so we use those instead of pulling in chi/gorilla.
func newServer(a *app) *http.ServeMux {
        mux := http.NewServeMux()

        // Library
        mux.HandleFunc("GET /", a.handleLibrary)

        // Add series form / submit
        mux.HandleFunc("GET /series/new", a.handleAddForm)
        mux.HandleFunc("POST /series/new", a.handleAddSubmit)

        // Series detail (read + quick progress + entry edit)
        mux.HandleFunc("GET /series/{id}", a.handleDetail)
        mux.HandleFunc("POST /series/{id}/progress", a.handleProgress)
        mux.HandleFunc("POST /series/{id}/entry", a.handleEntryEdit)

        // Series metadata edit form / submit
        mux.HandleFunc("GET /series/{id}/edit", a.handleEditForm)
        mux.HandleFunc("POST /series/{id}/edit", a.handleEditSubmit)

        // Cover upload / delete / set-by-URL
        mux.HandleFunc("POST /series/{id}/cover", a.handleCoverUpload)
        mux.HandleFunc("POST /series/{id}/cover/url", a.handleCoverURLSet)
        mux.HandleFunc("POST /series/{id}/cover/delete", a.handleCoverDelete)

        // Series delete (POST only — no GET form for destructive action)
        mux.HandleFunc("POST /series/{id}/delete", a.handleDelete)

        // Reading statistics dashboard
        mux.HandleFunc("GET /stats", a.handleStats)

        // Batch import (CSV/JSON) + export (backup / migration)
        mux.HandleFunc("GET /import", a.handleImportForm)
        mux.HandleFunc("POST /import", a.handleImportSubmit)
        mux.HandleFunc("GET /export/json", a.handleExportJSON)
        mux.HandleFunc("GET /export/csv", a.handleExportCSV)

        // JSON batch API — one request, one transaction for N entries
        mux.HandleFunc("POST /api/series/batch", a.handleBatchAPI)

        // UI preferences API — server-side (per-database) settings for
        // layout / ribbon / emblem / theme. See settings.go.
        mux.HandleFunc("GET /api/settings", a.handleSettingsGet)
        mux.HandleFunc("POST /api/settings", a.handleSettingsPost)

        // Theme toggle endpoint: stores the choice as a server-side setting
        // and redirects back (the preference follows the DB, not the browser).
        mux.HandleFunc("POST /theme", a.handleTheme)

        // Saved views ("shelves") — pin the current filter+sort combo as a
        // named shortcut. See settings.go.
        mux.HandleFunc("POST /shelves/save", a.handleShelfSave)
        mux.HandleFunc("POST /shelves/delete", a.handleShelfDelete)

        // Dropdown option lists (reading status / type / publication
        // status) — user-editable vocabularies. See options.go.
        mux.HandleFunc("GET /options", a.handleOptionsForm)
        mux.HandleFunc("POST /options/save", a.handleOptionsSave)

        // Bulk status changes — checkboxes on library cards + one POST.
        mux.HandleFunc("POST /bulk/status", a.handleBulkStatus)

        // One-click full backup: zipped DB snapshot + covers + restore notes.
        mux.HandleFunc("GET /backup", a.handleBackup)

        // Static files (CSS, JS, uploaded covers). The handler serves from
        // the working directory's static/ folder. http.FileServer strips
        // the /static/ prefix so URLs are /static/css/app.css etc.
        mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

        return mux
}

func main() {
        var (
                addr       = flag.String("addr", "127.0.0.1:4242", "listen address (host:port)")
                dbPath     = flag.String("db", "", "SQLite database path (default fic-tally.db)")
                tplDir     = flag.String("templates", "templates", "templates directory")
                staticDir  = flag.String("static", "static", "static assets directory")
        )
        flag.Parse()

        // Default DB name follows the app rename (Tsundoku → Fic Tally), but
        // an existing tsundoku.db keeps working: fall back to it when the
        // user hasn't passed -db and no fic-tally.db exists yet. No file is
        // renamed behind the user's back.
        if *dbPath == "" {
                *dbPath = "fic-tally.db"
                if _, err := os.Stat("fic-tally.db"); os.IsNotExist(err) {
                        if _, err2 := os.Stat("tsundoku.db"); err2 == nil {
                                *dbPath = "tsundoku.db"
                                log.Printf("using existing database tsundoku.db (delete or rename it to switch to fic-tally.db)")
                        }
                }
        }

        // Resolve coverDir as absolute so the upload handler can write to it
        // regardless of the process's working directory at request time.
        absStatic, err := filepath.Abs(*staticDir)
        if err != nil {
                log.Fatalf("resolve static dir: %v", err)
        }
        coverDir := filepath.Join(absStatic, "covers")
        if err := os.MkdirAll(coverDir, 0o755); err != nil {
                log.Fatalf("mkdir covers: %v", err)
        }

        store, err := NewSQLiteStore(*dbPath)
        if err != nil {
                log.Fatalf("open store: %v", err)
        }

        // The app struct exists before the templates are parsed because the
        // option-label template funcs close over it. initOptions loads (or
        // seeds) the dropdown vocabularies first — label rendering and every
        // downstream request depend on them.
        a := &app{store: store, coverDir: coverDir}
        a.initOptions()

        tpl, err := loadTemplates(a, *tplDir)
        if err != nil {
                log.Fatalf("load templates: %v", err)
        }
        a.tpl = tpl
        mux := newServer(a)

        log.Printf("fic-tally listening on http://%s/", *addr)
        srv := &http.Server{
                Addr: *addr,
                // noCache(mux) applies Cache-Control: no-cache to every response,
                // HTML pages included — see the noCache doc comment for why.
                Handler: noCache(mux),
                // ReadHeaderTimeout mitigates slowloris-style attacks even on a
                // localhost-only server, in case it's ever bound to a LAN.
                ReadHeaderTimeout: 5 * time.Second,
        }
        if err := srv.ListenAndServe(); err != nil {
                log.Fatalf("server: %v", err)
        }
}
