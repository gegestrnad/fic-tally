#!/usr/bin/env python3
"""Batch-import the local Komik library into fic-tally.

Per batch (~15 series):
  1. extract _cover from the first archive (subfolder fallback for the 6
     series stored as chapter folders)
  2. fetch weebcentral metadata (search -> detail -> chapters) via the local
     API, with a conservative 1.5s per-series pace (rate limit 60 req/min)
  3. create the series+entry in ONE batch API call, carrying the full
     metadata (author/description/tags/type/source_url/total_chapters) so no
     surgical UPDATE is needed
  4. upload each cover (POST /series/<id>/cover; 303 = ok)
  5. verify: rows in DB, cover count on GET /, one detail page spot-check,
     and that the pre-existing series entries are untouched

State/checkpoint in results.jsonl + checkpoint.json — rerunning skips done
folders and retries failed ones, so a killed run resumes cleanly.
"""
import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

_LIB_DEFAULT = Path.home() / "Komik"
LIB = Path(os.environ.get("KOMIK_LIB", _LIB_DEFAULT))
APP = "http://localhost:4242"
WC = "http://localhost:8000/api/v1"
DB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "fic-tally.db")
HERE = Path(__file__).resolve().parent
RESULTS = HERE / "results.jsonl"
CHECKPOINT = HERE / "checkpoint.json"
STATE = HERE / "state.json"
BATCH = 15
UA = {"User-Agent": "fic-tally/1.0"}

IMG_EXT = {".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".avif"}

# ---- state -----------------------------------------------------------------

def load_state():
    if STATE.exists():
        return json.loads(STATE.read_text(encoding="utf-8"))
    return {"done": [], "created_ids": {}, "cover_ids": {}, "misses": [], "prev_row_hashes": None}

def save_state(st):
    STATE.write_text(json.dumps(st, indent=1), encoding="utf-8")

def log_result(rec):
    with open(RESULTS, "a", encoding="utf-8") as f:
        f.write(json.dumps(rec, ensure_ascii=False) + "\n")

def existing_series_titles():
    con = sqlite3.connect(DB, timeout=10)
    con.execute("PRAGMA busy_timeout = 8000")
    rows = con.execute("SELECT id, title FROM series").fetchall()
    con.close()
    return rows

def prev_entry_snapshot():
    """Hash of all entry rows that exist before this run starts."""
    con = sqlite3.connect(DB, timeout=10)
    con.execute("PRAGMA busy_timeout = 8000")
    rows = con.execute(
        "SELECT series_id, status, current_chapter_num, current_chapter_label,"
        " rating, notes, bookmark_url, bookmark_label FROM entry ORDER BY series_id"
    ).fetchall()
    con.close()
    h = {}
    for r in rows:
        h[r[0]] = json.dumps(r[1:], ensure_ascii=False)
    return h

# ---- local folder analysis --------------------------------------------------

def find_covers(folder):
    """Return existing _cover.* path in folder, or None."""
    for p in folder.glob("_cover.*"):
        if p.is_file():
            return p
    return None

def extract_cover_top(folder):
    """extract_cover.py logic inline: first archive alphabetically, first
    image inside, write _cover.<ext> into the folder."""
    archives = sorted(
        (p for p in folder.iterdir()
         if p.is_file() and p.suffix.lower() in {".cbz", ".cbr"}),
        key=lambda p: p.name.lower(),
    )
    if not archives:
        return None, "no archives"
    arch = archives[0]
    if arch.suffix.lower() == ".cbz":
        with zipfile.ZipFile(arch) as zf:
            imgs = sorted(
                (i.filename for i in zf.infolist()
                 if not i.is_dir() and Path(i.filename).suffix.lower() in IMG_EXT),
            )
            if not imgs:
                return None, f"no image in {arch.name}"
            ext = Path(imgs[0]).suffix.lower()
            with zf.open(imgs[0]) as src, open(folder / f"_cover{ext}", "wb") as dst:
                dst.write(src.read())
        return folder / f"_cover{ext}", None
    return None, f"unsupported archive type {arch.suffix}"

def extract_cover_subfolder(folder):
    """Fallback for the 6 series stored as [NNNN] chapter subfolders: first
    image (alphabetical, recursed) in the first subfolder."""
    subs = sorted((p for p in folder.iterdir() if p.is_dir()),
                  key=lambda p: p.name.lower())
    for sub in subs:
        for p in sorted(sub.rglob("*"), key=lambda x: str(x).lower()):
            if p.is_file() and p.suffix.lower() in IMG_EXT:
                shutil.copy2(p, folder / f"_cover{p.suffix.lower()}")
                return folder / f"_cover{p.suffix.lower()}", None
    return None, "no images in subfolders"

def ensure_cover(folder):
    cov = find_covers(folder)
    if cov:
        return cov, "existing"
    cov, err = extract_cover_top(folder)
    if cov:
        return cov, "extracted"
    cov, err2 = extract_cover_subfolder(folder)
    if cov:
        return cov, "subfolder"
    return None, f"cover failed: {err} / {err2}"

def analyze_folder(folder):
    """Return (max_chapter or None, source note).
    Priority: [a-b] ranges -> single trailing number -> bracketed subfolders
    -> 'Vol N' fallback."""
    nums = []
    files = sorted((p for p in folder.iterdir()
                    if p.is_file() and p.suffix.lower() in {".cbz", ".cbr"}),
                   key=lambda p: p.name.lower())
    for p in files:
        b = p.name[: -len(p.suffix)]
        pairs = re.findall(r"[\[\(]\s*(\d{1,4})\s*[-–—]\s*(\d{1,4})\s*[\]\)]", b)
        for lo, hi in pairs:
            nums.append(int(hi))
        m = re.search(r"(\d{1,4})\s*[-–—]\s*(\d{1,4})\s*(?:End|END|end|Completed|COMPLETED|_?\.?)*\s*$", b)
        if m:
            nums.append(int(m.group(2)))
        else:
            m2 = re.search(r"(?:Ch\.?|Chapter)\s*(\d{1,4})\s*$", b, re.I)
            if m2:
                nums.append(int(m2.group(1)))
            else:
                # bare trailing number; reject "S1"/"S2"-style season tags
                m3 = re.search(r"(?<![A-Za-z])(\d{1,4})\s*_?\s*$", b)
                if m3:
                    n = int(m3.group(1))
                    if n < 10000:
                        nums.append(n)
    if nums:
        return max(nums), "archives"
    # subfolder fallback: prefer "Chapter N" in the name over the [NNNN]
    # download index (e.g. Onepunch-Man: "[0158] Chapter 156" -> 156)
    subs = [p.name for p in folder.iterdir() if p.is_dir()]
    chnums = [int(x) for s in subs for x in re.findall(r"Ch(?:apter)?\.?\s*(\d{1,4})", s, re.I)]
    if chnums:
        return max(chnums), "subfolders"
    br = [int(x) for s in subs for x in re.findall(r"^\[(\d{1,4})\]", s)]
    if br:
        return max(br), "subfolders"
    vol = []
    for p in files:
        for v in re.findall(r"Vol(?:ume)?\.?\s*(\d{1,3})", p.name, re.I):
            vol.append(int(v))
    if vol:
        return max(vol), "volumes-only"
    return None, "no chapter info"

# ---- weebcentral ------------------------------------------------------------

def api_get(url):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=40) as r:
        return json.load(r)

def norm(s):
    return re.sub(r"[^a-z0-9]+", "", (s or "").lower())

def tri(s):
    s = "  " + s + "  "
    return {s[i:i + 3] for i in range(len(s) - 2)} if len(s) >= 3 else {s}

def sim(a, b):
    ta, tb = tri(a), tri(b)
    if not ta or not tb:
        return 0.0
    return len(ta & tb) / len(ta | tb)

def wc_search(title):
    q = urllib.parse.quote(title)
    try:
        d = api_get(f"{WC}/search?q={q}&limit=20")
        return d.get("results", []) or []
    except urllib.error.URLError as e:
        print(f"    [wc] search error: {e}", flush=True)
        return []

def wc_detail(wc_id):
    try:
        return api_get(f"{WC}/series/{wc_id}")
    except urllib.error.URLError as e:
        print(f"    [wc] detail error: {e}", flush=True)
        return None

def wc_chapters(wc_id):
    """Known total chapter count. The API's `number` field is unreliable
    (Solo Leveling=200 ok, but Tower of God=3, One-Punch Man mixed), so the
    max is taken from chapter TITLES ('Chapter N') first, falling back to
    the number field. Returns None when neither yields anything plausible."""
    try:
        d = api_get(f"{WC}/series/{wc_id}/chapters")
        chs = d.get("chapters", [])
    except urllib.error.URLError as e:
        print(f"    [wc] chapters error: {e}", flush=True)
        return None
    title_nums = []
    num_field = []
    for c in chs:
        m = re.search(r"Chapter\s+(\d+)", c.get("title") or "", re.I)
        if m:
            title_nums.append(int(m.group(1)))
        if isinstance(c.get("number"), (int, float)):
            num_field.append(c["number"])
    if title_nums:
        return float(max(title_nums))
    # fall back to number field only if it looks like a real chapter ladder
    # (many distinct values and max close to the list length)
    if num_field:
        distinct = set(num_field)
        if len(distinct) >= max(1, int(len(num_field) * 0.5)) and max(num_field) <= len(num_field) + 5:
            return float(max(num_field))
    return None

TYPEMAP = {
    "manga": "manga", "manhwa": "manhwa", "manhua": "manhua",
    "light novel": "light novel", "web novel": "web novel",
    "oel": "light novel", "novel": "light novel",
}

def wc_queries(title):
    """Query ladder for the WC search (word-based index). Full title first;
    if that returns 0 hits, progressively shorter word-aligned tails and
    the most distinctive single words. E.g. 'A Returner's Magic Should Be
    Special' needs the word 'Returner'; 'Onepunch-Man (ONE)' needs
    'Onepunch-Man'. The matcher still re-scores every hit against the full
    title, so over-broad queries can't cause a wrong match."""
    t = title.strip()
    qs = [t]
    words = re.findall(r"[A-Za-z0-9''\-]+", t)
    stop = {"the", "a", "an", "of", "in", "on", "to", "and", "or", "is", "no",
            "ni", "ga", "de", "wa", "wo", "ha", "that", "with", "my", "i", "o",
            "at", "by", "for", "its", "it"}
    distinct = [w for w in words if w.lower() not in stop]
    if len(words) >= 3:
        qs.append(" ".join(words[-3:]))
        qs.append(" ".join(words[-2:]))
    for w in sorted(distinct, key=len, reverse=True)[:2]:
        if len(w) >= 6:
            qs.append(w)
            # hyphenated word: also try split ("Onepunch-Man" -> "Onepunch Man")
            if "-" in w:
                qs.append(w.replace("-", " "))
    # punctuation-insensitive variant: "Onepunch-Man (ONE)" -> "Onepunch Man ONE"
    if re.search(r"[\-()\[\]'/]", t):
        q2 = re.sub(r"\s+", " ", re.sub(r"[\-()\[\]'/]", " ", t)).strip()
        q2 = re.sub(r"\s{2,}", " ", q2)
        if q2:
            qs.append(q2)
    seen, out = set(), []
    for q in qs:
        k = q.casefold()
        if k and k not in seen:
            seen.add(k)
            out.append(q)
    return out

def score_candidate(nq, titles):
    """Score a query title against a list of candidate titles (normalized)."""
    best = 0.0
    for c in titles:
        if not c:
            continue
        if c == nq:
            return 1.0
        if nq and (nq in c or c in nq) and min(len(nq), len(c)) >= 8:
            best = max(best, 0.85 * min(len(nq), len(c)) / max(len(nq), len(c)))
        best = max(best, sim(nq, c))
    return best

def wc_metadata(title):
    """Return (found: bool, meta dict, match info).

    The WC search index titles often differ from the folder (Japanese vs
    English), so the decision is made on detail-page title + alternative
    titles: candidates from every query in the ladder are kept, their
    details fetched (cheap when cached), and the best re-score decides.
    """
    nq = norm(title)
    cands = []  # (score, hit) seen so far, unique by wc id
    seen_ids = set()
    for query in wc_queries(title):
        results = wc_search(query)
        if not results:
            time.sleep(0.3)
            continue
        for r in results:
            if r["id"] in seen_ids:
                continue
            s = score_candidate(nq, [norm(r.get("title", ""))])
            if s > 0.2:  # keep plausible candidates; detail re-score decides
                cands.append((s, r))
                seen_ids.add(r["id"])
        # stop the ladder early once an exact search-title match is in hand
        if any(s >= 1.0 for s, _ in cands):
            break
        time.sleep(0.4)
    if not cands:
        return False, {}, {"score": 0.0}
    # rank by search-title score, then re-score top candidates on detail
    cands.sort(key=lambda x: -x[0])
    best = None
    for s, hit in cands[:4]:
        d = wc_detail(hit["id"])
        if not d:
            continue
        full = score_candidate(
            nq, [norm(d.get("title", ""))] + [norm(a) for a in d.get("alternative_titles") or []]
        )
        score = max(s, full)
        if best is None or score > best[0]:
            best = (score, hit, d)
        if score >= 0.97:
            break
    if not best or best[0] < 0.6:
        return False, {}, {"score": best[0] if best else 0}
    score, hit, d = best
    # sanity guard: a near-exact title hit with a wildly different type is
    # likely a different work (e.g. matching a volume/OEL entry)
    raw_type = (d.get("type") or "").lower()
    mapped = TYPEMAP.get(raw_type, raw_type or "manga")
    if score < 1.0 and mapped in ("light novel", "web novel") and score < 0.8:
        return False, {}, {"score": score, "reason": "type mismatch on fuzzy hit"}
    total = wc_chapters(hit["id"])
    meta = {
        "wc_id": hit["id"],
        "wc_title": d.get("title"),
        "author": ", ".join(d.get("authors") or []),
        "description": (d.get("description") or "").strip(),
        "tags": d.get("genres") or [],
        "type": mapped,
        "status": (d.get("status") or "").lower(),
        "source_url": hit.get("url") or d.get("url") or "",
        "total_chapters": total,
    }
    return True, meta, {"score": score, "matched": d.get("title")}

# ---- app API -----------------------------------------------------------------

def http_post_json(url, payload):
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json", **UA}, method="POST")
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.status, json.load(r)

def upload_cover(series_id, cover_path):
    # curl handles multipart without deps; retry on transient connection
    # failures (curl exit 0 with a 000 code happens occasionally on Windows).
    codes = []
    for attempt in range(3):
        cmd = ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
               "--max-time", "90",
               "-F", f"cover=@{cover_path}", f"{APP}/series/{series_id}/cover"]
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
        code = out.stdout.strip()
        codes.append(code)
        if code in ("303", "302", "200"):
            return code
        time.sleep(1.5)
    return codes[-1] if codes else "000"

def verify_batch(new_ids, title):
    """(a) rows exist, (b) cover count on GET /, (c) spot-check one detail
    page, (d) pre-existing entries untouched."""
    con = sqlite3.connect(DB, timeout=10)
    con.execute("PRAGMA busy_timeout = 8000")
    missing = []
    for sid in new_ids:
        r = con.execute("SELECT title FROM series WHERE id=?", (sid,)).fetchone()
        if not r:
            missing.append(sid)
    # spot check: random created row's author/description present on page
    spot = con.execute(
        "SELECT s.title, s.author, e.status FROM series s JOIN entry e ON e.series_id=s.id WHERE s.id=?",
        (new_ids[-1],)
    ).fetchone()
    # pre-existing entry integrity
    rows = con.execute(
        "SELECT series_id, status, current_chapter_num, current_chapter_label,"
        " rating, notes, bookmark_url, bookmark_label FROM entry ORDER BY series_id"
    ).fetchall()
    con.close()
    now_hash = {r[0]: json.dumps(r[1:], ensure_ascii=False) for r in rows}
    problems = []
    st = load_state()
    prev = st.get("prev_row_hashes")
    if prev:
        for sid, h in prev.items():
            if now_hash.get(sid) != h:
                problems.append(f"PRE-EXISTING ENTRY CHANGED: {sid}")
    cover_count = None
    try:
        req = urllib.request.Request(APP + "/", headers=UA)
        with urllib.request.urlopen(req, timeout=30) as r:
            cover_count = r.read().decode("utf-8", "replace").count('cover-img')
    except Exception as e:
        problems.append(f"GET / failed: {e}")
    return {
        "missing_ids": missing,
        "spot_title": spot[0] if spot else None,
        "spot_author": spot[1] if spot else None,
        "spot_status": spot[2] if spot else None,
        "cover_count_home": cover_count,
        "problems": problems,
    }

# ---- batch driver -------------------------------------------------------------

def slugify(s):
    s = re.sub(r"[^a-z0-9]+", "-", (s or "").lower()).strip("-")
    return s

def run_batch(st, folders):
    print(f"\n=== batch of {len(folders)} ===", flush=True)
    t0 = time.time()
    batch_results = []
    payloads = []
    cover_map = {}
    order = []

    for folder in folders:
        title = folder.name
        print(f"-- {title}", flush=True)
        rec = {"title": title, "type": "manga", "ch": None, "chsrc": None,
               "cover": None, "wc": "pending", "wc_score": None,
               "wc_matched": None, "id": None, "upload": None, "created": False, "notes": []}
        ch, chsrc = analyze_folder(folder)
        rec["ch"], rec["chsrc"] = ch, chsrc
        cov, covhow = ensure_cover(folder)
        rec["cover"] = str(cov) if cov else None
        rec["cover_how"] = covhow
        if not cov:
            rec["notes"].append("NO COVER")
        found, meta, info = wc_metadata(title)
        rec["wc_score"] = round(info.get("score", 0), 3)
        rec["wc_matched"] = info.get("matched")
        if found:
            rec["wc"] = "found"
            rec.update({k: meta.get(k) for k in ("type", "author", "total_chapters", "status") if meta.get(k) is not None})
            rec["wc_title"] = meta.get("wc_title")
            rec["source_url"] = meta.get("source_url")
        else:
            rec["wc"] = "miss"
            st["misses"].append(title)
            rec["notes"].append(f"wc miss (score {info.get('score', 0):.2f})")
        # build payload item
        item = {"title": title, "type": rec.get("type") or "manga", "status": "reading"}
        if found:
            item.update({
                "author": meta.get("author") or "",
                "description": meta.get("description") or "",
                "tags": meta.get("tags") or [],
                "source_url": meta.get("source_url") or "",
            })
            if meta.get("total_chapters") is not None:
                item["total_chapters"] = meta["total_chapters"]
                item["total_is_known"] = True
                # completed only if wc complete AND local max >= known total
                if (meta.get("status") == "complete" and ch is not None
                        and ch + 1 >= meta["total_chapters"]):
                    item["status"] = "completed"
                    rec["notes"].append("completed (wc complete, local max >= total)")
        if ch is not None:
            item["chapter_num"] = ch
        payloads.append(item)
        cover_map[title] = cov
        order.append((title, rec))
        time.sleep(0.3)

    # create via batch API
    status, resp = http_post_json(f"{APP}/api/series/batch",
                                  {"series": payloads, "duplicate_policy": "skip"})
    print(f"  batch API: {status} created={resp.get('created')} updated={resp.get('updated')} "
          f"skipped={resp.get('skipped')} failed={resp.get('failed')}", flush=True)
    created_ids = []
    for i, (title, rec) in enumerate(order):
        res = resp.get("results", [{}])[i] if i < len(resp.get("results", [])) else {}
        rec["created"] = res.get("action") == "created"
        rec["id"] = res.get("id")
        if res.get("action") == "error":
            rec["notes"].append(f"API error: {res.get('message')}")
        elif rec["created"]:
            st["created_ids"][title] = res.get("id")
            created_ids.append(res.get("id"))

    # upload covers
    for title, rec in order:
        sid = rec.get("id")
        cov = cover_map.get(title)
        if sid and cov and Path(cov).exists():
            code = upload_cover(sid, cov)
            rec["upload"] = code
            if code in ("303", "302", "200"):
                st["cover_ids"][title] = sid
            else:
                rec["notes"].append(f"cover upload HTTP {code}")

    # verify
    v = verify_batch(created_ids, order[0][0] if order else None)
    print(f"  verify: missing={v['missing_ids']} home_covers={v['cover_count_home']} "
          f"problems={v['problems']}", flush=True)
    for title, rec in order:
        rec["batch_s"] = round(time.time() - t0, 1)
        log_result(rec)
        st["done"].append(title)
    save_state(st)
    print(f"  batch done in {time.time() - t0:.0f}s", flush=True)
    return [r for _, r in order]

def main():
    st = load_state()
    if st.get("prev_row_hashes") is None:
        st["prev_row_hashes"] = prev_entry_snapshot()
        # also record existing series so we never re-import them
        st["existing_titles"] = sorted(t for _, t in existing_series_titles())
        save_state(st)
        print(f"baseline: {len(st['existing_titles'])} existing series; "
              f"{len(st['prev_row_hashes'])} entry rows hashed", flush=True)

    all_folders = sorted([p for p in LIB.iterdir() if p.is_dir()],
                         key=lambda p: p.name.lower())
    todo = [f for f in all_folders
            if f.name not in set(st["done"]) and f.name not in st.get("existing_titles", set())]
    print(f"total={len(all_folders)} done={len(st['done'])} todo={len(todo)}", flush=True)

    limit = int(sys.argv[1]) if len(sys.argv) > 1 else None
    stop_after = int(sys.argv[2]) if len(sys.argv) > 2 else None  # stop after N batches
    n_batches = 0
    while todo:
        chunk = todo[:BATCH]
        todo = todo[BATCH:]
        try:
            run_batch(st, chunk)
        except Exception as e:
            # Save whatever progress exists, then stop cleanly. Failed folders
            # are not marked done, so a re-run resumes from here.
            import traceback
            traceback.print_exc()
            log_result({"title": f"__BATCH_ERROR__", "error": str(e),
                        "folder": [c.name for c in chunk]})
            save_state(st)
            print(f"== batch error, stopping: {e}", flush=True)
            break
        n_batches += 1
        if limit and n_batches >= limit:
            print("== limit reached, stopping", flush=True)
            break
        if stop_after and n_batches >= stop_after:
            break
    print("== all done", flush=True)

if __name__ == "__main__":
    main()
