#!/usr/bin/env python3
"""Fetch metadata from weebcentral API and fill fic-tally database."""
import json
import os
import sqlite3
import time
import urllib.parse
import urllib.request
import sys

API_BASE = "http://127.0.0.1:8000/api/v1"
DB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fic-tally.db")

def search_wc(title, limit=5):
    """Search weebcentral for a series by title."""
    encoded = urllib.parse.quote(title)
    url = f"{API_BASE}/search?q={encoded}&limit={limit}"
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode())
    except Exception as e:
        print(f"  ERROR search '{title}': {e}")
        return None

def get_series(series_id):
    """Get full series metadata by ID."""
    url = f"{API_BASE}/series/{series_id}"
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode())
    except Exception as e:
        print(f"  ERROR get '{series_id}': {e}")
        return None

def find_best_match(search_results, title):
    """Pick the best matching series from search results."""
    if not search_results or "results" not in search_results or not search_results["results"]:
        return None
    
    title_lower = title.lower()
    best = None
    best_score = -1
    
    for result in search_results["results"]:
        result_title = result.get("title", "").lower()
        # Exact match gets highest score
        if result_title == title_lower:
            return result
        # Starts with match
        elif result_title.startswith(title_lower):
            if len(result_title) < len(best.get("title", "")) if best else True:
                best = result
        # Contains match
        elif title_lower in result_title or result_title in title_lower:
            if best is None:
                best = result
    
    return best

def normalize_status(status):
    """Normalize WC status to fic-tally format."""
    if not status:
        return ""
    status_lower = status.lower()
    if "complete" in status_lower or "finished" in status_lower:
        return "Completed"
    elif "ongoing" in status_lower or "publishing" in status_lower:
        return "Ongoing"
    elif "hiatus" in status_lower or "on hiatus" in status_lower:
        return "On hiatus"
    elif "cancelled" in status_lower or "discontinued" in status_lower:
        return "Cancelled"
    elif "upcoming" in status_lower:
        return "Upcoming"
    return status.title()

def normalize_type(series_type):
    """Normalize WC type to fic-tally format."""
    if not series_type:
        return ""
    type_lower = series_type.lower()
    if type_lower == "manhwa":
        return "manhwa"
    elif type_lower == "manga":
        return "manga"
    elif type_lower == "manhua":
        return "manhua"
    elif "light novel" in type_lower:
        return "light novel"
    elif "web novel" in type_lower:
        return "web novel"
    return series_type

def main():
    # Load series needing metadata
    with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "series_needing_meta.json"), "r") as f:
        series_list = json.load(f)
    
    print(f"Total series to process: {len(series_list)}")
    
    conn = sqlite3.connect(DB, timeout=30)
    conn.execute("PRAGMA journal_mode=WAL")
    
    success = 0
    failed = 0
    skipped = 0
    
    for i, series in enumerate(series_list):
        series_id = series["id"]
        title = series["title"]
        source_url = series.get("source_url", "")
        
        # If we already have a source_url with weebcentral ID, use it directly
        wc_id = None
        if source_url and "weebcentral.com/series/" in source_url:
            # Extract ID from URL: https://weebcentral.com/series/{ID}/slug
            parts = source_url.split("/series/")[1] if "/series/" in source_url else ""
            wc_id = parts.split("/")[0] if parts else None
        
        print(f"[{i+1}/{len(series_list)}] {title}")
        
        # If we don't have the WC ID, search for it
        if not wc_id:
            search_result = search_wc(title)
            if not search_result:
                print(f"  SKIPPED (search failed)")
                skipped += 1
                continue
            
            best = find_best_match(search_result, title)
            if not best:
                print(f"  SKIPPED (no match)")
                skipped += 1
                continue
            
            wc_id = best["id"]
            print(f"  Found WC ID: {wc_id}")
        
        # Get full series metadata
        series_data = get_series(wc_id)
        if not series_data:
            print(f"  SKIPPED (fetch failed)")
            skipped += 1
            continue
        
        # Extract fields
        series_info = series_data.get("series", series_data)
        
        alt_titles = series_info.get("alternative_titles", [])
        if alt_titles:
            alt_titles_json = json.dumps(alt_titles, ensure_ascii=False)
        else:
            alt_titles_json = "[]"
        
        type_val = normalize_type(series_info.get("type", ""))
        authors = series_info.get("authors", [])
        author = ", ".join(authors) if authors else ""
        pub_status = normalize_status(series_info.get("status", ""))
        year = series_info.get("release_year", 0) or 0
        tags = series_info.get("genres", [])
        if tags:
            tags_json = json.dumps(tags, ensure_ascii=False)
        else:
            tags_json = "[]"
        description = series_info.get("description", "")
        new_source_url = series_info.get("url", source_url)
        
        # Update database
        try:
            conn.execute("""
                UPDATE series
                SET alt_titles = ?, type = ?, author = ?, pub_status = ?,
                    year = ?, tags = ?, description = ?, source_url = ?
                WHERE id = ?
            """, (alt_titles_json, type_val, author, pub_status, year,
                  tags_json, description, new_source_url, series_id))
            conn.commit()
            success += 1
            print(f"  ✓ Updated: type={type_val}, status={pub_status}, year={year}, authors={author}")
        except Exception as e:
            print(f"  ERROR updating: {e}")
            conn.rollback()
            failed += 1
        
        # Rate limiting
        time.sleep(0.3)
    
    print(f"\n{'='*60}")
    print(f"SUMMARY:")
    print(f"  Total: {len(series_list)}")
    print(f"  Success: {success}")
    print(f"  Failed: {failed}")
    print(f"  Skipped: {skipped}")
    
    conn.close()

if __name__ == "__main__":
    main()
