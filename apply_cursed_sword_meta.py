#!/usr/bin/env python3
"""Populate fic-tally metadata for 'A Cursed Sword's Daily Life' from the
weebcentral-api (localhost:8000), following the apply_wc_meta.py convention:
surgically touch only author/description/tags/type/source_url + the stale
'Metadata unverified' entry note. Does NOT touch cover_url, title,
total_chapters, total_is_known, created_at, parent_id, or reading progress."""
import json
import os
import sqlite3
import urllib.request

API = "http://localhost:8000/api/v1"
UID = "01J76XYDCX8YZVTSY1WRJNQAFM"
SID = "a-cursed-sword-s-daily-life"
SLUG_URL = "https://weebcentral.com/series/01J76XYDCX8YZVTSY1WRJNQAFM/The-Whimsical-Cursed-Sword"
DB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fic-tally.db")
TYPEMAP = {"Manga": "manga", "Manhwa": "manhwa", "Manhua": "manhua"}


def api_get(path):
    req = urllib.request.Request(API + path, headers={"User-Agent": "fic-tally/1.0"})
    with urllib.request.urlopen(req, timeout=40) as r:
        return json.load(r)


def main():
    d = api_get(f"/series/{UID}")
    ch = api_get(f"/series/{UID}/chapters")

    author = ", ".join(d.get("authors", []))
    tags_json = json.dumps(d.get("genres", []), ensure_ascii=False)
    raw_type = d.get("type", "") or ""
    typ = TYPEMAP.get(raw_type) or TYPEMAP.get(raw_type.capitalize()) or raw_type.lower() or "manga"
    desc = (d.get("description") or "").strip()

    n_ch = len(ch.get("chapters", []))
    max_num = max((c["number"] for c in ch.get("chapters", [])), default=None)

    print("Fetched from API:")
    print(f"  title      = {d.get('title')}")
    print(f"  authors    = {d.get('authors')}")
    print(f"  genres     = {d.get('genres')}")
    print(f"  type       = {d.get('type')} -> {typ}")
    print(f"  status     = {d.get('status')}, release_year={d.get('release_year')}")
    print(f"  alt_titles = {d.get('alternative_titles')}")
    print(f"  chapters   = {n_ch} (max number {max_num})")

    con = sqlite3.connect(DB, timeout=10)
    con.execute("PRAGMA busy_timeout = 8000")
    cur = con.cursor()
    cur.execute(
        "UPDATE series SET author=?, description=?, tags=?, type=?, source_url=? WHERE id=?",
        (author, desc, tags_json, typ, SLUG_URL, SID),
    )
    n_series = cur.rowcount

    note_done = ""
    cur.execute("SELECT notes FROM entry WHERE series_id=?", (SID,))
    row = cur.fetchone()
    if row and "Metadata unverified" in (row[0] or ""):
        cur.execute(
            "UPDATE entry SET notes=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE series_id=?",
            ("Metadata from Weeb Central (2026-08-27): " + SLUG_URL, SID),
        )
        note_done = " note-updated"

    con.commit()
    con.close()
    print(f"\nApplied: series_rows={n_series} type={typ} author='{author}' "
          f"tags={len(json.loads(tags_json))} desc_chars={len(desc)}{note_done}")


if __name__ == "__main__":
    main()
