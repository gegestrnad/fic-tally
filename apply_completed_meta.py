#!/usr/bin/env python3
"""Apply full weebcentral metadata to the 46 'completed series' added from
D:\\completed series links.txt.

For each weeb ID:
  - update the Series row: title (canonical), author, year, total_chapters,
    total_is_known, tags (genres), description, cover_url, alt_titles, type,
    pub_status=completed
  - ensure an Entry row exists with status=completed + completed_at
  - create the Series+Entry fresh if missing (The Breaker: New Waves)

Only touches the 46 target series (matched by weeb ID in source_url).
"""
import json
import os
import re
import sqlite3
import time

DB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fic-tally.db")
META = os.path.join(os.path.dirname(os.path.abspath(__file__)), "wc_meta_completed.json")

# weebcentral type -> fic-tally type
TYPEMAP = {"manga": "manga", "manhwa": "manhwa", "manhua": "manhua",
           "light novel": "light novel", "web novel": "web novel"}
# weebcentral status -> fic-tally pub_status
STATUSMAP = {"complete": "completed", "ongoing": "ongoing",
             "hiatus": "hiatus", "cancelled": "cancelled", "upcoming": "upcoming"}


def slugify(title: str) -> str:
    s = title.lower()
    s = re.sub(r"[^a-z0-9]+", "-", s)
    return s.strip("-")


def norm(t: str) -> str:
    return re.sub(r"\s+", " ", t.strip().lower())


meta = json.load(open(META, encoding="utf-8"))

con = sqlite3.connect(DB, timeout=15)
con.execute("PRAGMA foreign_keys=ON")
cur = con.cursor()

now = time.strftime("%Y-%m-%d %H:%M:%S")
created = updated = 0
report = []

for wid, entry in meta.items():
    s = entry["series"]
    chap_count = entry["chapter_count"]

    title = s["title"].strip()
    authors = ", ".join(s.get("authors", []))
    year = s.get("release_year") or 0
    genres = s.get("genres", [])
    description = s.get("description") or ""
    cover = (s.get("cover") or {}).get("url", "")
    wc_type = s.get("type") or "manga"
    ftype = TYPEMAP.get(wc_type, "manga")
    pub_status = STATUSMAP.get(s.get("status"), "completed")

    # alt titles: weebcentral's list, minus anything identical to the main title
    main_norm = norm(title)
    alt_titles = [a for a in s.get("alternative_titles", [])
                  if norm(a) and norm(a) != main_norm]

    source_url = s.get("url", f"https://weebcentral.com/series/{wid}")

    # find existing series by weeb id in source_url
    cur.execute("SELECT id FROM series WHERE source_url LIKE ?", (f"%{wid}%",))
    row = cur.fetchone()

    if row:
        sid = row[0]
        cur.execute("""
            UPDATE series SET
                title=?, author=?, description=?, cover_url=?, tags=?,
                type=?, year=?, pub_status=?, total_chapters=?, total_is_known=?,
                alt_titles=?, source_url=?
            WHERE id=?
        """, (title, authors, description, cover, json.dumps(genres, ensure_ascii=False),
              ftype, year, pub_status, float(chap_count), 1,
              json.dumps(alt_titles, ensure_ascii=False), source_url, sid))
        # ensure entry
        cur.execute("SELECT 1 FROM entry WHERE series_id=?", (sid,))
        if cur.fetchone():
            cur.execute("""
                UPDATE entry SET status='completed',
                    completed_at=CASE WHEN completed_at='' THEN ? ELSE completed_at END,
                    updated_at=?
                WHERE series_id=?
            """, (now, now, sid))
        else:
            cur.execute("""
                INSERT INTO entry (series_id, status, completed_at, updated_at, last_read_at)
                VALUES (?,?,?,?,?)
            """, (sid, "completed", now, now, now))
        updated += 1
        action = "updated"
    else:
        sid = slugify(title)
        # avoid id collision
        base = sid
        n = 2
        cur.execute("SELECT 1 FROM series WHERE id=?", (sid,))
        while cur.fetchone():
            sid = f"{base}-{n}"
            n += 1
            cur.execute("SELECT 1 FROM series WHERE id=?", (sid,))
        cur.execute("""
            INSERT INTO series (id, title, type, author, description, cover_url,
                tags, source_url, total_chapters, total_is_known, created_at,
                parent_id, alt_titles, year, pub_status)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        """, (sid, title, ftype, authors, description, cover,
              json.dumps(genres, ensure_ascii=False), source_url,
              float(chap_count), 1, now, "",
              json.dumps(alt_titles, ensure_ascii=False), year, pub_status))
        cur.execute("""
            INSERT INTO entry (series_id, status, completed_at, updated_at, last_read_at)
            VALUES (?,?,?,?,?)
        """, (sid, "completed", now, now, now))
        created += 1
        action = "created"

    report.append((action, sid, title, ftype, year, chap_count,
                   len(alt_titles), len(authors), bool(cover)))

con.commit()

print(f"Created: {created}  Updated: {updated}  Total: {created+updated}\n")
print(f"{'action':<8} {'id':<42} {'title':<40} {'type':<8} {'yr':<5} {'ch':<5} {'alt':<4} {'auth':<5} cov")
for action, sid, title, ftype, year, ch, nalt, nauth, hascov in report:
    print(f"{action:<8} {sid:<42} {title[:39]:<40} {ftype:<8} {year:<5} {ch:<5} {nalt:<4} {nauth:<5} {'Y' if hascov else 'N'}")

con.close()
