package main

// backup.go — one-click full backup (GET /backup).
//
// The JSON/CSV exports cover the series+entry data, but not the rest of the
// database: UI settings (layout, ribbon, emblem, theme, shelves, default
// sort), the daily_reads streak counters, or the per-series chapter_log
// reading history. And the covers live on disk, outside any export.
//
// This endpoint bundles EVERYTHING into a single zip:
//
//      fic-tally.db   — a consistent snapshot of the whole database
//                       (SQLite VACUUM INTO: clean, compacted, WAL-folded)
//      covers/*       — every uploaded cover image, byte-for-byte
//      RESTORE.txt    — what this archive is and how to restore it
//
// The database snapshot is written to a temp file (VACUUM INTO refuses to
// overwrite an existing path), streamed into the zip, and deleted. The zip
// itself is streamed straight to the response — a large cover library never
// buffers entirely in memory.

import (
        "archive/zip"
        "fmt"
        "io"
        "log"
        "net/http"
        "os"
        "path/filepath"
        "time"
)

// handleBackup serves GET /backup — downloads a zip with the database
// snapshot, all covers, and restore instructions.
func (a *app) handleBackup(w http.ResponseWriter, r *http.Request) {
        // 1. Consistent DB snapshot via the store (VACUUM INTO under SQLite).
        tmp, err := os.CreateTemp("", "fic-tally-snapshot-*.db")
        if err != nil {
                a.serverError(w, r, "create snapshot temp file", err)
                return
        }
        snapshotPath := tmp.Name()
        tmp.Close()
        os.Remove(snapshotPath) // VACUUM INTO requires the destination NOT to exist
        defer os.Remove(snapshotPath)

        if err := a.store.Snapshot(snapshotPath); err != nil {
                a.serverError(w, r, "snapshot database", err)
                return
        }

        // 2. Stream the zip: headers first so the browser offers a download
        // with a timestamped filename instead of rendering garbage.
        filename := "fic-tally-backup-" + time.Now().UTC().Format("20060102-150405") + ".zip"
        w.Header().Set("Content-Type", "application/zip")
        w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
        w.Header().Set("X-Content-Type-Options", "nosniff")

        zw := zip.NewWriter(w)
        defer zw.Close()

        // 2a. The database snapshot.
        if err := addFileToZip(zw, snapshotPath, "fic-tally.db"); err != nil {
                // Headers are already sent; all we can do is log and cut the
                // stream short. The user sees a truncated download and the
                // server log carries the reason.
                log.Printf("[error] backup: add db snapshot: %v", err)
                return
        }

        // 2b. Every cover, preserving the flat <id>.<ext> layout so a restore
        // is a plain copy back into static/covers/.
        if err := filepath.Walk(a.coverDir, func(path string, info os.FileInfo, err error) error {
                if err != nil {
                        return err
                }
                if info.IsDir() {
                        return nil
                }
                rel, err := filepath.Rel(a.coverDir, path)
                if err != nil {
                        return err
                }
                return addFileToZip(zw, path, filepath.ToSlash(filepath.Join("covers", rel)))
        }); err != nil {
                log.Printf("[warn] backup: walk covers: %v", err)
                // Keep going — a missing/unreadable cover shouldn't void the
                // database snapshot already written.
        }

        // 2c. Restore instructions, so the archive is self-describing even
        // after it has left the machine.
        restore := fmt.Sprintf(
                "Fic Tally full backup\n"+
                        "Created: %s (UTC)\n"+
                        "App: fic-tally (single Go binary, SQLite storage)\n"+
                        "\n"+
                        "Contents\n"+
                        "  fic-tally.db  Complete database: series, tracking entries, tags,\n"+
                        "                UI settings (layout/ribbon/emblem/theme/shelves),\n"+
                        "                daily read counters, per-series reading history.\n"+
                        "  covers/       Uploaded cover images (series-id named files).\n"+
                        "\n"+
                        "Restore\n"+
                        "  1. Stop the fic-tally server.\n"+
                        "  2. Replace the server's database file with fic-tally.db\n"+
                        "     (default path: fic-tally.db next to the binary;\n"+
                        "     check the -db flag you run with).\n"+
                        "  3. Copy covers/* into the server's static/covers/ directory.\n"+
                        "  4. Start the server again. Everything — library, history,\n"+
                        "     streaks, preferences — is exactly as it was.\n"+
                        "\n"+
                        "Note: fic-tally.db here is a compacted snapshot (VACUUM INTO),\n"+
                        "not a live copy; it is fully consistent and opens directly.\n",
                time.Now().UTC().Format("2006-01-02 15:04:05"))
        if err := addBytesToZip(zw, []byte(restore), "RESTORE.txt"); err != nil {
                log.Printf("[error] backup: add RESTORE.txt: %v", err)
                return
        }

        if err := zw.Close(); err != nil {
                log.Printf("[error] backup: finalize zip: %v", err)
        }
}

// addFileToZip streams one disk file into the archive under name.
func addFileToZip(zw *zip.Writer, path, name string) error {
        f, err := os.Open(path)
        if err != nil {
                return err
        }
        defer f.Close()
        info, err := f.Stat()
        if err != nil {
                return err
        }
        hdr, err := zip.FileInfoHeader(info)
        if err != nil {
                return err
        }
        hdr.Name = name
        hdr.Method = zip.Deflate
        // Covers are already-compressed images; Deflate still helps the text
        // rows and costs little. The DB compresses well (~60-70%).
        zf, err := zw.CreateHeader(hdr)
        if err != nil {
                return err
        }
        _, err = io.Copy(zf, f)
        return err
}

// addBytesToZip writes an in-memory blob into the archive under name.
func addBytesToZip(zw *zip.Writer, data []byte, name string) error {
        zf, err := zw.CreateHeader(&zip.FileHeader{
                Name:     name,
                Method:   zip.Deflate,
                Modified: time.Now().UTC(),
        })
        if err != nil {
                return err
        }
        _, err = zf.Write(data)
        return err
}
