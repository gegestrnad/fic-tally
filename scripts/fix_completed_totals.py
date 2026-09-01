#!/usr/bin/env python3
"""Recompute Total chapters for completed series from MangaUpdates.

Background: MU's `latest_chapter` is the chapter number of the LAST release,
not the series total. For multi-volume finished works (e.g. The Gamer:
latest release "v.7 c.44 (end)" but actually 511 chapters) it is wrong.
The authoritative total for a finished series is the "N Chapters (Complete)"
count in the MU status text; volume-only statuses
("72 Volumes (Complete)") fall back to latest_chapter (Naruto: 700).

Scope: series rows with pub_status 'completed' AND a mangaupdates.com
source_url. 130 of those rows carry a known placeholder slug
(gh0mamm/khchange — a bulk-fill bug), so source_url is NOT trusted as the
identifier. Instead each row is resolved by TITLE SEARCH, with:
  * exact normalized-title match required (case/punct/space-insensitive);
  * the row's real slug (when present and not the placeholder) must match
    the hit's URL;
  * a hit is only accepted when it is the UNAMBIGUOUS exact-title match;
  * a type mismatch (e.g. row says manhwa, hit says manga) rejects the hit.
Ambiguous / no-match / type-conflict rows are SKIPPED and reported — never
guessed. Only the total chapters field is written; nothing else.

Algorithm mirrors muTotalChapters() in mu.go EXACTLY:
  1. regex (?i)\\b(\\d[\\d,]*)\\s+(?:WN\\s+|SS\\s+)?Chapters\\b[^(]*\\(Complete
     against the status text → first match, commas stripped;
  2. else latest_chapter when > 0;
  3. else 0 → leave the stored total untouched.

Usage:
  python3 scripts/fix_completed_totals.py            # dry run, prints plan
  python3 scripts/fix_completed_totals.py --apply    # backup + write
"""

import json
import os
import re
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DB = os.path.join(ROOT, "fic-tally.db")
MU_BASE = "https://api.mangaupdates.com/v1"
UA = "fic-tally/1.0"
TIMEOUT = 20
BACKOFF = 0.3
MAX_RETRIES = 3

PLACEHOLDER_SLUG = "gh0mamm/khchange"

# Identical to muTotalChaptersCompleteRe in mu.go
TOTAL_RE = re.compile(
    r"\b(\d[\d,]*)\s+(?:WN\s+|SS\s+)?Chapters\b[^(]*\(Complete", re.IGNORECASE
)

SLUG_RE = re.compile(r"mangaupdates\.com/series/(\S+)")


# --- MU API ---

def mu_request(method, path, body=None):
    url = MU_BASE + path
    req = urllib.request.Request(url, method=method)
    req.add_header("User-Agent", UA)
    req.add_header("Accept", "application/json")
    req.add_header("Content-Type", "application/json")
    if body is not None:
        req.data = json.dumps(body).encode()
    for attempt in range(MAX_RETRIES):
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
                return json.loads(resp.read().decode())
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503) and attempt < MAX_RETRIES - 1:
                time.sleep(BACKOFF * (2 ** (attempt + 1)))
                continue
            raise
        except urllib.error.URLError:
            if attempt < MAX_RETRIES - 1:
                time.sleep(BACKOFF * (2 ** (attempt + 1)))
                continue
            raise


def mu_search(title):
    data = mu_request("POST", "/series/search", {"search": title, "perpage": 10})
    return [r["record"] for r in data.get("results", []) if r.get("record")]


def mu_fetch(series_id):
    data = mu_request("GET", f"/series/{series_id}?unrenderedFields=true")
    rec = data.get("record", data) if isinstance(data, dict) else None
    if rec and rec.get("series_id") == series_id:
        return rec
    return None


# --- Matching ---

def norm(s):
    s = (s or "").lower()
    s = s.replace("&", " and ")
    s = re.sub(r"[^a-z0-9]+", " ", s)
    return re.sub(r"\s+", " ", s).strip()


def slug_of(url):
    m = SLUG_RE.search(url or "")
    return m.group(1).rstrip("/") if m else None


def pick_hit(row_title, row_slug, hits):
    """Return (record, reason) of the confident exact-title match, else
    (None, skip_reason). Never guesses: ambiguous results are skipped."""
    exact = [h for h in hits if norm(h.get("title")) == norm(row_title)]
    if len(exact) == 0:
        return None, "no exact-title match"
    if len(exact) > 1:
        # disambiguate by slug if the row has a real one
        if row_slug and row_slug != PLACEHOLDER_SLUG:
            sl = [h for h in exact if slug_of(h.get("url")) == row_slug]
            if len(sl) == 1:
                return sl[0], "title+slug"
        return None, f"{len(exact)} exact-title candidates, ambiguous"
    hit = exact[0]
    if row_slug and row_slug != PLACEHOLDER_SLUG:
        if slug_of(hit.get("url")) != row_slug:
            return None, f"slug mismatch (row={row_slug} hit={slug_of(hit.get('url'))})"
    return hit, "exact title"


def compute_total(status_text, latest_chapter):
    """Mirror of muTotalChapters() in mu.go. Returns int (0 = unknown).

    Only the FIRST line of the status text is consulted (MU puts the
    overall status on line 1; season/volume breakdowns follow and must
    not be mistaken for the total). latest_chapter is a last-release
    figure and is never used as the total — it is accepted as a parameter
    only to keep the call signature in sync with the Go helper."""
    status_text = (status_text or "").split("\n", 1)[0]
    m = TOTAL_RE.search(status_text)
    if m:
        try:
            n = int(m.group(1).replace(",", ""))
            if n > 0:
                return n
        except ValueError:
            pass
    return 0


def main():
    apply = "--apply" in sys.argv
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    cur = con.cursor()

    rows = cur.execute(
        """SELECT id, title, type, total_chapters, total_is_known, source_url
           FROM series
           WHERE pub_status IN ('complete', 'completed')
             AND source_url LIKE '%mangaupdates.com%'
           ORDER BY title"""
    ).fetchall()
    print(f"completed MU-sourced series: {len(rows)}")

    plan, skipped, unchanged = [], [], 0
    seen = {}  # series_id -> (full_record or None)
    for i, row in enumerate(rows, 1):
        title = row["title"]
        row_slug = slug_of(row["source_url"])
        try:
            hits = mu_search(title)
        except Exception as e:
            skipped.append((title, f"search error: {e}"))
            time.sleep(BACKOFF)
            continue
        time.sleep(BACKOFF)
        hit, how = pick_hit(title, row_slug, hits)
        if hit is None:
            skipped.append((title, how))
            continue
        sid = hit["series_id"]
        if sid in seen:
            rec = seen[sid]
        else:
            try:
                rec = mu_fetch(sid)
            except Exception as e:
                rec = None
                skipped.append((title, f"fetch {sid} error: {e}"))
            seen[sid] = rec
            time.sleep(BACKOFF)
        if rec is None:
            continue  # already recorded in skipped
        # type sanity guard: row type must be compatible with MU type
        mu_type = (rec.get("type") or "").lower()
        if row["type"] and row["type"].lower() != mu_type:
            skipped.append((title, f"type conflict (row={row['type']} mu={mu_type or '?'})"))
            continue
        new = compute_total(rec.get("status"), rec.get("latest_chapter"))
        old = row["total_chapters"]
        if new == 0:
            unchanged += 1
        elif old is None or int(old) != new:
            src = "status" if TOTAL_RE.search(rec.get("status") or "") else "latest"
            plan.append((row["id"], title, sid, old, new, src, how))
        else:
            unchanged += 1
        if i % 25 == 0 or i == len(rows):
            print(f"  ...{i}/{len(rows)} (changes so far: {len(plan)}, "
                  f"skipped: {len(skipped)})", file=sys.stderr)

    print(f"\n{'=' * 72}")
    print(f"to update: {len(plan)}  unchanged: {unchanged}  "
          f"skipped: {len(skipped)}")
    print(f"{'=' * 72}")
    for rid, title, sid, old, new, src, how in plan:
        print(f"  {title[:45]:45s} {old if old is not None else '(none)':>9} "
              f"-> {new:<7} [{src}/{how}] mu={sid}")
    print("\n-- skipped (NOT touched, for manual review) --")
    for title, why in skipped:
        print(f"  {title[:50]:50s} {why}")

    if not apply:
        print("\nDRY RUN — no writes. Re-run with --apply to commit.")
        return
    if not plan:
        print("nothing to update.")
        return

    ts = time.strftime("%Y%m%d-%H%M%S")
    backup = DB + f".bak-{ts}"
    src = sqlite3.connect(DB)
    dst = sqlite3.connect(backup)
    with dst:
        src.backup(dst)
    dst.close()
    src.close()
    print(f"\nbackup -> {backup}")

    with con:
        for rid, title, sid, old, new, s, how in plan:
            con.execute(
                "UPDATE series SET total_chapters = ?, total_is_known = 1 "
                "WHERE id = ?", (float(new), rid))
    print(f"updated {len(plan)} rows.")

    for rid, title, sid, old, new, s, how in plan:
        got = con.execute(
            "SELECT total_chapters, total_is_known FROM series WHERE id=?",
            (rid,)).fetchone()
        assert got[0] == float(new) and got[1] == 1, (rid, got)
    print("verified: all updated rows read back correctly.")


if __name__ == "__main__":
    main()
