package main

// api.go — the JSON batch endpoint, POST /api/series/batch.
//
// One request carries N entries; the server validates everything, resolves
// duplicates, and writes all rows in ONE transaction (Store.SaveAll). That's
// the efficiency answer to "one HTTP request per entry": bulk input is now
// 1 request + 1 transaction total, regardless of N (capped at 1000/request).
//
// Request (either shape):
//
//      {"series":[ {...}, {...} ], "duplicate_policy":"skip", "dry_run":false}
//      [ {...}, {...} ]
//
// Each item uses the same field names as the JSON export (plus chapter_num
// as an alias for current_chapter_number). Response:
//
//      {"created":n,"updated":n,"skipped":n,"failed":n,
//       "results":[{"index":0,"title":"...","id":"...","action":"created","message":""}]}

import (
        "encoding/json"
        "io"
        "net/http"
        "strconv"
        "strings"
)

const apiMaxItems = 1000

type batchRequest struct {
        Series         []importItem `json:"series"`
        DuplicatePolicy string      `json:"duplicate_policy"`
        DryRun         bool         `json:"dry_run"`
}

type batchResponse struct {
        Created int            `json:"created"`
        Updated int            `json:"updated"`
        Skipped int            `json:"skipped"`
        Failed  int            `json:"failed"`
        DryRun  bool           `json:"dry_run,omitempty"`
        Results []importResult `json:"results"`
}

// handleBatchAPI implements POST /api/series/batch.
func (a *app) handleBatchAPI(w http.ResponseWriter, r *http.Request) {
        r.Body = http.MaxBytesReader(w, r.Body, 4<<20)

        // Accept both {"series":[...],...} and a bare [...] — decide from the
        // first non-space byte, then decode the whole body once.
        body, err := io.ReadAll(r.Body)
        if err != nil {
                writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
                return
        }
        trimmed := strings.TrimLeft(string(body), " \t\r\n")
        if trimmed == "" {
                writeJSONError(w, http.StatusBadRequest, "empty body")
                return
        }

        var req batchRequest
        switch trimmed[0] {
        case '[':
                if err := json.Unmarshal([]byte(trimmed), &req.Series); err != nil {
                        writeJSONError(w, http.StatusBadRequest, "invalid JSON array: "+err.Error())
                        return
                }
        default:
                if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
                        writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
                        return
                }
        }
        if req.Series == nil {
                writeJSONError(w, http.StatusBadRequest, `missing "series" array (expected {"series":[...]} or [...])`)
                return
        }
        if len(req.Series) > apiMaxItems {
                writeJSONError(w, http.StatusBadRequest, strconv.Itoa(len(req.Series))+" items exceeds the "+strconv.Itoa(apiMaxItems)+"-item-per-request cap")
                return
        }

        switch req.DuplicatePolicy {
        case "", "skip", "update", "create":
        default:
                writeJSONError(w, http.StatusBadRequest, "duplicate_policy must be one of: skip, update, create")
                return
        }
        if req.DuplicatePolicy == "" {
                req.DuplicatePolicy = "skip"
        }

        existing, err := a.store.List()
        if err != nil {
                writeJSONError(w, http.StatusInternalServerError, "list existing: "+err.Error())
                return
        }

        batch, results, sum := resolveImport(req.Series, existing, req.DuplicatePolicy, req.DryRun, a.options())
        if !req.DryRun && len(batch) > 0 {
                if err := a.store.SaveAll(batch); err != nil {
                        writeJSONError(w, http.StatusInternalServerError, "save batch: "+err.Error())
                        return
                }
        }

        w.Header().Set("Content-Type", "application/json; charset=utf-8")
        w.WriteHeader(http.StatusOK)
        enc := json.NewEncoder(w)
        enc.SetIndent("", "  ")
        _ = enc.Encode(batchResponse{
                Created: sum.Created,
                Updated: sum.Updated,
                Skipped: sum.Skipped,
                Failed:  sum.Failed,
                DryRun:  req.DryRun,
                Results: results,
        })
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
        w.Header().Set("Content-Type", "application/json; charset=utf-8")
        w.WriteHeader(code)
        _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
