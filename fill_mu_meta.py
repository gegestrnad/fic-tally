#!/usr/bin/env python3
"""Fill missing metadata for fic-tally series using MangaUpdates API.

Searches api.mangaupdates.com for each series by title, maps the results
to fic-tally's Series fields (author, type, year, pub_status, description,
tags, cover_url, alt_titles, source_url), and updates the database.

Only touches Series fields — Entry rows (user tracking data) are never changed.
"""

import json
import math
import os
import re
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# --- Constants ---
DB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fic-tally.db")
MU_BASE = "https://api.mangaupdates.com/v1"
TIMEOUT = 15
BACKOFF = 0.3  # seconds between requests (optimistic but MU tolerates it)
MAX_RESULTS = 5  # max search hits to check per series
MAX_RETRIES = 1

# MU type -> fic-tally type mapping (same as mu.go muTypeMap)
TYPE_MAP = {
    "doujinshi": "doujinshi",
    "light novel": "light novel",
    "manga": "manga",
    "manhua": "manhua",
    "manhwa": "manhwa",
    "web novel": "web novel",
    "webtoon": "webtoon",
    "novel": "novel",
    "oel": "oel",
}

# MU status text -> fic-tally pub_status (same as mu.go muPubStatus)
def map_pub_status(completed, status_text):
    """Derive fic-tally pub_status from MU completed flag and status text."""
    if completed is True:
        return "completed"
    s = status_text.strip().lower() if status_text else ""
    if not s:
        return ""
    if "ongoing" in s:
        return "ongoing"
    if "hiatus" in s:
        return "hiatus"
    if "cancel" in s or "discontinu" in s:
        return "cancelled"
    return ""


# --- MU API Client ---

def mu_request(method, path, body=None):
    """Make a request to the MU API with retry logic."""
    url = MU_BASE + path
    req = urllib.request.Request(url, method=method)
    req.add_header("User-Agent", "fic-tally/1.0")
    req.add_header("Accept", "application/json")
    req.add_header("Content-Type", "application/json")
    
    if body is not None:
        req.data = json.dumps(body).encode()

    for attempt in range(MAX_RETRIES + 1):
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
                return json.loads(resp.read().decode())
        except urllib.error.HTTPError as e:
            if e.code == 429 and attempt < MAX_RETRIES:
                time.sleep(BACKOFF * (attempt + 1))
                continue
            raise
        except urllib.error.URLError as e:
            if attempt < MAX_RETRIES:
                time.sleep(BACKOFF * (attempt + 1))
                continue
            raise


def mu_search(title):
    """Search MU by title, return list of (series_id, record).
    
    The MU API uses {"search": "..."} for the query parameter.
    """
    try:
        data = mu_request("POST", "/series/search", {"search": title})
        results = data.get("results", [])
        return [(r.get("record", {}).get("series_id"), r.get("record"))
                for r in results if r.get("record", {}).get("series_id")]
    except Exception as e:
        print(f"  Search error for '{title}': {e}", file=sys.stderr)
        return []


def mu_fetch_details(series_id):
    """Fetch full details for a series by ID."""
    try:
        data = mu_request("GET", f"/series/{series_id}?unrenderedFields=true")
        # The fetch endpoint returns the record at the top level
        if data and data.get("series_id") == series_id:
            return data
        # Also handle the case where it's wrapped in "record" (for search results)
        record = data.get("record")
        if record and record.get("series_id") == series_id:
            return record
        print(f"  No record found for series_id={series_id}", file=sys.stderr)
        return None
    except Exception as e:
        print(f"  Fetch error for series_id={series_id}: {e}", file=sys.stderr)
        return None


# --- Mapping Functions ---

def mu_map_type(mu_type):
    """Map MU type to fic-tally type."""
    mapped = TYPE_MAP.get(mu_type.strip().lower(), "")
    return mapped


def mu_map_author(authors):
    """Pick the author name from MU authors list."""
    for a in authors:
        if a.get("type", "").lower() == "author":
            return a.get("name", "").strip()
    if authors:
        return authors[0].get("name", "").strip()
    return ""


def mu_map_years(rec):
    """Extract year from MU record."""
    year_str = rec.get("year", "")
    if year_str:
        try:
            return int(year_str)
        except (ValueError, TypeError):
            pass
    return 0


def mu_map_tags(genres):
    """Build tag list from MU genres."""
    seen = set()
    out = []
    for g in genres:
        gt = g.get("genre", "").strip()
        if gt and gt.lower() not in seen:
            seen.add(gt.lower())
            out.append(gt)
    return out


def mu_map_alt_titles(associated):
    """Build alt_titles list from MU associated titles."""
    if not associated:
        return []
    titles = [t.get("title", "").strip() for t in associated if t.get("title")]
    return [t for t in titles if t]


# --- Database Operations ---

def get_missing_series(con):
    """Get series with missing metadata that can be filled from MU."""
    cur = con.cursor()
    cur.execute("""
        SELECT id, title, author, type, description, year, pub_status, 
               cover_url, source_url, alt_titles, tags
        FROM series
        WHERE author IS NULL OR author = ''
           OR description IS NULL OR description = ''
           OR type IS NULL OR type = ''
           OR year IS NULL OR year = 0
           OR pub_status IS NULL OR pub_status = ''
    """)
    return cur.fetchall()


def get_user_options(con):
    """Get user-configured type and pub_status option lists from settings."""
    cur = con.cursor()
    cur.execute("SELECT value FROM settings WHERE key = 'options'")
    row = cur.fetchone()
    if row:
        try:
            opts = json.loads(row[0])
            types = [o["value"].strip().lower() for o in opts.get("type", [])]
            statuses = [o["value"].strip().lower() for o in opts.get("status", [])]
            return types, statuses
        except (json.JSONDecodeError, TypeError, KeyError):
            pass
    return ["manga", "manhwa", "manhua", "light novel", "web novel"], ["ongoing", "completed", "hiatus", "cancelled", "on hold", "dropped", "plan to read"]


def update_series(con, series_id, updates):
    """Update a series row with the given field values."""
    if not updates:
        return False
    
    set_clauses = []
    values = []
    for field, value in updates.items():
        set_clauses.append(f"{field} = ?")
        values.append(value)
    
    values.append(series_id)
    query = f"UPDATE series SET {', '.join(set_clauses)} WHERE id = ?"
    
    cur = con.cursor()
    cur.execute(query, values)
    con.commit()
    return cur.rowcount > 0


# --- Main ---

def main():
    print(f"Connecting to database: {DB}")
    if not os.path.exists(DB):
        print(f"ERROR: Database not found at {DB}")
        sys.exit(1)
    
    con = sqlite3.connect(DB)
    con.execute("PRAGMA foreign_keys = ON")
    
    # Get valid option lists
    valid_types, valid_statuses = get_user_options(con)
    
    # Get series needing metadata
    missing = get_missing_series(con)
    print(f"Found {len(missing)} series with missing metadata")
    
    if not missing:
        print("All series already have complete metadata.")
        con.close()
        return
    
    # Process each series
    updated_count = 0
    skipped_count = 0
    failed_count = 0
    
    for i, row in enumerate(missing, 1):
        series_id = row[0]
        title = row[1]
        existing_author = row[2] or ""
        existing_type = row[3] or ""
        existing_description = row[4] or ""
        existing_year = row[5] or 0
        existing_pub_status = row[6] or ""
        existing_cover_url = row[7] or ""
        existing_source_url = row[8] or ""
        existing_alt_titles = row[9] or ""
        existing_tags = row[10] or ""
        
        print(f"\n[{i}/{len(missing)}] Processing: {title}")
        print(f"  Existing: author={bool(existing_author)}, type={bool(existing_type)}, "
              f"desc={bool(existing_description)}, year={existing_year}, "
              f"status={bool(existing_pub_status)}")
        
        # Search MU
        results = mu_search(title)
        if not results:
            print(f"  No MU results found")
            failed_count += 1
            time.sleep(BACKOFF)
            continue
        
        # Find the best match - prefer exact title match first, then by rating
        best_rec = None
        best_id = None
        
        for sid, rec in results[:MAX_RESULTS]:
            mu_title = rec.get("title", "").strip().lower()
            fic_title = title.strip().lower()
            
            # Exact match gets priority
            if mu_title == fic_title:
                best_rec = rec
                best_id = sid
                break
        
        # If no exact match, pick highest rated
        if not best_rec:
            best_result = max(results[:MAX_RESULTS], 
                            key=lambda x: x[1].get("bayesian_rating", 0) or 0)
            best_id, best_rec = best_result
        
        if not best_rec:
            print(f"  Could not find best match")
            failed_count += 1
            time.sleep(BACKOFF)
            continue
        
        # Fetch full details with unrendered fields
        full_rec = mu_fetch_details(best_id)
        if not full_rec:
            print(f"  Could not fetch full details")
            failed_count += 1
            time.sleep(BACKOFF)
            continue
        
        # Map fields
        updates = {}
        any_update = False
        
        # Author
        if not existing_author:
            author = mu_map_author(full_rec.get("authors", []))
            if author:
                updates["author"] = author
                any_update = True
        
        # Type (only if empty and maps to valid option)
        if not existing_type:
            mu_type = full_rec.get("type", "")
            mapped_type = mu_map_type(mu_type)
            if mapped_type and mapped_type.lower() in [t.lower() for t in valid_types]:
                updates["type"] = mapped_type
                any_update = True
        
        # Year
        if not existing_year or existing_year == 0:
            year = mu_map_years(full_rec)
            if year > 0:
                updates["year"] = year
                any_update = True
        
        # Pub status
        if not existing_pub_status:
            completed = full_rec.get("completed")
            status_text = full_rec.get("status", "")
            pub_status = map_pub_status(completed, status_text)
            if pub_status and pub_status.lower() in [s.lower() for s in valid_statuses]:
                updates["pub_status"] = pub_status
                any_update = True
        
        # Description
        if not existing_description:
            desc = full_rec.get("description", "").strip()
            if desc:
                updates["description"] = desc
                any_update = True
        
        # Cover URL (only if no existing cover)
        if not existing_cover_url:
            image = full_rec.get("image", {})
            cover_url = image.get("url", {}).get("original", "")
            if cover_url:
                updates["cover_url"] = cover_url
                any_update = True
        
        # Source URL
        if not existing_source_url:
            source_url = full_rec.get("url", "")
            if source_url:
                updates["source_url"] = source_url
                any_update = True
        
        # Alt titles
        if not existing_alt_titles:
            associated = full_rec.get("associated", [])
            alt = mu_map_alt_titles(associated)
            if alt:
                updates["alt_titles"] = json.dumps(alt)
                any_update = True
        
        # Tags (merge with existing)
        existing_tag_list = []
        if existing_tags:
            try:
                existing_tag_list = json.loads(existing_tags)
            except (json.JSONDecodeError, TypeError):
                existing_tag_list = []
        
        mu_tags = full_rec.get("genres", [])
        new_tags = mu_tag_union(existing_tag_list, mu_tags)
        if new_tags != existing_tag_list:
            updates["tags"] = json.dumps(new_tags)
            any_update = True
        
        if any_update:
            if update_series(con, series_id, updates):
                print(f"  ✓ Updated: {list(updates.keys())}")
                updated_count += 1
            else:
                print(f"  ✗ Failed to update")
                failed_count += 1
        else:
            print(f"  - No new data to apply")
            skipped_count += 1
        
        # Rate limit
        time.sleep(BACKOFF)
    
    con.close()
    
    print(f"\n{'='*60}")
    print(f"SUMMARY:")
    print(f"  Updated: {updated_count}")
    print(f"  Skipped (no new data): {skipped_count}")
    print(f"  Failed: {failed_count}")
    print(f"  Total processed: {len(missing)}")
    print(f"{'='*60}")
    
    if updated_count > 0:
        print(f"\n✓ Successfully updated {updated_count} series with MU metadata")
    else:
        print(f"\n⚠ No series were updated")


def mu_tag_union(existing_tags, mu_genres):
    """Merge existing tags with MU genres (existing first, deduped case-insensitive)."""
    seen = set(t.lower() for t in existing_tags)
    out = list(existing_tags)
    
    for g in mu_genres:
        genre_name = g.get("genre", "").strip()
        if genre_name and genre_name.lower() not in seen:
            seen.add(genre_name.lower())
            out.append(genre_name)
    
    return out


if __name__ == "__main__":
    main()
