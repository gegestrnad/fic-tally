#!/usr/bin/env python3
"""Surgically update fic-tally series metadata from weebcentral.

Only touches series metadata columns (author, description, tags, type,
source_url) and the stale 'Metadata unverified' entry notes. Deliberately
does NOT touch cover_url, title, total_chapters, total_is_known,
created_at, parent_id, or any reading-progress entry fields
(status/current_chapter_num/current_chapter_label/rating/bookmark_*).
"""
import json
import os
import sqlite3

DB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fic-tally.db")

# weebcentral series uid -> fic-tally series id
IDMAP = {
    "01J76XYFQ7MSZ82JJVJNEPQDGE": "99-reinforced-wooden-stick",
    "01J76XYD2BYZWK1HT1FYCZFS74": "100-nin-no-eiyuu-o-sodateta-saikyou-yogensha-wa-boukensha-ni-natte-mo-sekaijuu-no-deshi-kara-shitawarete-masu",
    "01J76XYAVZXJDQCS4RPQ4NYVMY": "2001-5",
    "01KZNTEDZ1RKB6NK6JVTQFHDXA": "30-years-since-the-prologue",
    "01JCTZD5MM8103XB73QFQD6PRW": "4-cut-hero",
    "01J76XY86MP2D7D2EZNMX72A8H": "51-ways-to-save-my-girlfriend",
    "01J76XYEZZ9J2810N80XATRD3V": "66-666-years-advent-of-the-dark-mage",
    "01J76XYCMEBPRYBN4JRA4G1W8Q": "a-dating-sim-of-life-or-death",
}

TYPEMAP = {"Manga": "manga", "Manhwa": "manhwa", "Manhua": "manhua"}

# series ids whose entry.notes carry the stale 'Metadata unverified' line and
# should be refreshed now that weebcentral is the source.
FIX_NOTE_IDS = {
    "99-reinforced-wooden-stick",
    "2001-5",
    "30-years-since-the-prologue",
    "4-cut-hero",
    "66-666-years-advent-of-the-dark-mage",
    "a-dating-sim-of-life-or-death",
}

meta = json.load(open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "wc_meta.json"), encoding="utf-8"))
by_url = {m["url"]: m for m in meta}

con = sqlite3.connect(DB, timeout=10)
con.execute("PRAGMA busy_timeout = 8000")
cur = con.cursor()

report = []
for uid, sid in IDMAP.items():
    # find the matching wc record by uid in its url
    m = next((x for x in meta if f"/{uid}/" in x["url"]), None)
    if not m:
        report.append((sid, "NO WC DATA"))
        continue

    author = ", ".join(m.get("authors", []))
    tags_json = json.dumps(m.get("tags", []), ensure_ascii=False)
    typ = TYPEMAP.get(m.get("type", ""), "manga")
    desc = m.get("description", "").strip()
    source = m["url"]

    cur.execute(
        """UPDATE series
           SET author=?, description=?, tags=?, type=?, source_url=?
           WHERE id=?""",
        (author, desc, tags_json, typ, source, sid),
    )
    n_series = cur.rowcount

    note_done = ""
    if sid in FIX_NOTE_IDS:
        cur.execute("SELECT notes FROM entry WHERE series_id=?", (sid,))
        row = cur.fetchone()
        if row and "Metadata unverified" in (row[0] or ""):
            new_note = "Metadata from Weeb Central (2026-08-27): " + source
            cur.execute(
                "UPDATE entry SET notes=?, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') "
                "WHERE series_id=?",
                (new_note, sid),
            )
            note_done = "note-updated"

    report.append((sid, f"series_rows={n_series} type={typ} author='{author}' "
                        f"tags={len(json.loads(tags_json))} {note_done}"))

con.commit()
con.close()

for sid, info in report:
    print(f"{info}\n   {sid}")
