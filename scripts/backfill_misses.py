#!/usr/bin/env python3
"""Backfill pass: re-match the WC misses with the improved matcher and
UPDATE the already-created series via the batch API (policy=update).

For each miss we carry the EXISTING entry fields (status, chapter) back into
the payload so the update doesn't clobber them — it only adds author /
description / tags / type / source_url / total_chapters.
"""
import json
import sqlite3
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import populate_komik as pk

DB = pk.DB
BATCH = 15

def main():
    recs = []
    for line in open(pk.RESULTS, encoding="utf-8"):
        r = json.loads(line)
        if r.get("title") == "__BATCH_ERROR__":
            continue
        recs.append(r)
    misses = [r for r in recs if r.get("wc") == "miss" and r.get("id")]
    print(f"misses to backfill: {len(misses)}", flush=True)

    con = sqlite3.connect(DB, timeout=10)
    con.execute("PRAGMA busy_timeout = 8000")

    fixed, still_miss = [], []
    i = 0
    while i < len(misses):
        chunk = misses[i:i + BATCH]
        i += BATCH
        payloads = []
        for r in chunk:
            found, meta, info = pk.wc_metadata(r["title"])
            if not found:
                still_miss.append((r["title"], info.get("score", 0)))
                time.sleep(0.3)
                continue
            # carry existing entry fields back
            row = con.execute(
                "SELECT status, current_chapter_num FROM entry WHERE series_id=?",
                (r["id"],),
            ).fetchone()
            status, chnum = row if row else ("reading", None)
            item = {"title": r["title"], "type": meta.get("type") or "manga",
                    "status": status,
                    "author": meta.get("author") or "",
                    "description": meta.get("description") or "",
                    "tags": meta.get("tags") or [],
                    "source_url": meta.get("source_url") or ""}
            if chnum is not None:
                item["chapter_num"] = chnum
            if meta.get("total_chapters") is not None:
                item["total_chapters"] = meta["total_chapters"]
                item["total_is_known"] = True
            payloads.append((r, item))
            print(f"  backfill: {r['title'][:50]:50} -> {meta.get('wc_title')} "
                  f"type={meta.get('type')} total={meta.get('total_chapters')}", flush=True)
            time.sleep(0.5)
        if payloads:
            status_code, resp = pk.http_post_json(
                f"{pk.APP}/api/series/batch",
                {"series": [p for _, p in payloads], "duplicate_policy": "update"})
            print(f"  backfill API: {status_code} updated={resp.get('updated')} "
                  f"created={resp.get('created')} failed={resp.get('failed')}", flush=True)
            for r, _ in payloads:
                fixed.append(r["title"])
    con.close()

    print(f"\nfixed={len(fixed)} still_miss={len(still_miss)}", flush=True)
    for t, s in still_miss:
        print(f"  still miss: {t[:60]:60} score={s:.2f}")
    # log outcomes
    with open(pk.HERE / "backfill_results.jsonl", "a", encoding="utf-8") as f:
        for t in fixed:
            f.write(json.dumps({"title": t, "action": "backfilled"}, ensure_ascii=False) + "\n")
        for t, s in still_miss:
            f.write(json.dumps({"title": t, "action": "still_miss", "score": s}, ensure_ascii=False) + "\n")

if __name__ == "__main__":
    main()
