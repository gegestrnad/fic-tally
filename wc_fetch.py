#!/usr/bin/env python3
"""Fetch weebcentral series pages and dump clean metadata as JSON.

Targets the real server-rendered HTML:
  - full synopsis:  <p class="whitespace-pre-wrap break-words">...</p>
  - meta list:      <ul class="flex flex-col gap-4"> with <li><strong>Label</strong>...</li>
"""
import html as htmlmod
import json
import re
import sys
import urllib.request

UA = {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                  "(KHTML, like Gecko) Chrome/126.0 Safari/537.36",
    "Accept": "text/html, */*",
}


def fetch(url: str) -> str:
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=25) as r:
        return r.read().decode("utf-8", "ignore")


def strip(s: str) -> str:
    s = re.sub(r"<[^>]+>", "", s or "")
    return re.sub(r"[ \t]+", " ", htmlmod.unescape(s)).strip()


def meta_value(page: str, label: str) -> str:
    """Get the inner content of a <li><strong>Label</strong>...</li> block."""
    pat = r"<strong>" + re.escape(label) + r"</strong>(.*?)(?:</li>|<strong>)"
    m = re.search(pat, page, re.S)
    return strip(m.group(1)) if m else ""


def meta_names(page: str, label: str) -> list[str]:
    """Get the <li> sub-items of a multi-value meta block (e.g. Associated Name)."""
    pat = r"<strong>" + re.escape(label) + r"</strong>(.*?)(?:</li>\s*</li>|</ul>)"
    m = re.search(pat, page, re.S)
    if not m:
        return []
    items = re.findall(r"<li>(.*?)</li>", m.group(1), re.S)
    return [strip(i) for i in items if strip(i)]


def link_values(page: str, query_key: str) -> list[str]:
    """Extract de-duplicated link values for search?KEY= links (authors, tags)."""
    raw = re.findall(re.escape(query_key) + r'=([^"&]+)', page)
    vals, seen = [], set()
    for v in raw:
        v = htmlmod.unescape(v).replace("+", " ")
        if v not in seen:
            seen.add(v)
            vals.append(v)
    return vals


def parse(url: str) -> dict:
    page = fetch(url)
    out = {"url": url}

    m = re.search(r"<title>([^|]+)\|", page)
    if m:
        out["title"] = htmlmod.unescape(m.group(1)).strip()

    # Full synopsis
    m = re.search(
        r'<p class="whitespace-pre-wrap break-words">(.*?)</p>', page, re.S)
    if m:
        out["description"] = strip(m.group(1))

    out["authors"] = link_values(page, "author")
    out["tags"] = link_values(page, "included_tag")

    def label_value(label: str) -> str:
        # <strong>Label</strong> or <strong>Label: </strong> followed by a value
        # (either a linked <a> or plain text) up to the closing </li>.
        m = re.search(
            r"<strong>\s*" + re.escape(label) + r":?\s*</strong>(.*?)(?:</li>|$)",
            page, re.S)
        if not m:
            return ""
        return strip(m.group(1))

    typ = label_value("Type")
    if typ in ("Manga", "Manhwa", "Manhua", "OEL"):
        out["type"] = typ
    else:
        m = re.search(r"included_type=([^\"&]+)", page)
        if m:
            out["type"] = htmlmod.unescape(m.group(1)).replace("+", " ")

    status = label_value("Status")
    if status in ("Ongoing", "Complete", "Hiatus", "Canceled"):
        out["status"] = status

    released = label_value("Released")
    if released:
        out["released"] = released

    assoc = meta_names(page, "Associated Name(s)")
    if assoc:
        out["aliases"] = assoc

    return out


if __name__ == "__main__":
    results = []
    for url in sys.argv[1:]:
        try:
            results.append(parse(url))
        except Exception as e:
            results.append({"url": url, "error": str(e)})
    print(json.dumps(results, indent=2, ensure_ascii=False))
