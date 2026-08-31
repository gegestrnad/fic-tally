# Fic Tally — Reading Tracker

A lightweight, self-hosted manga/novel reading tracker. Single user, no
accounts, no forums, no release feeds.

> Formerly named **Tsundoku** — renamed to Fic Tally. An existing
> `tsundoku.db` is picked up automatically on first run (nothing is renamed
> behind your back); new databases are `fic-tally.db`.

## What's inside

- **Library** — cover grid with the ribbon progress indicator, search,
  filter by status/type/tag, sort by last read / title / rating / updated.
- **Progress tracking** — chapter number + label (handles "Extra 1",
  "Vol. 4 Ch. 2"), quick +1, bookmark as a labeled resume point, 1-10
  rating, notes/review per series.
- **Covers** — upload a file or **paste a URL** on the edit page.
- **Reading stats** (`/stats`) — currently reading, completed this month,
  average rating, reading streak (+ longest), status/type breakdowns, top
  tags, and a 30-day activity strip.
- **Series grouping** — link spinoffs/prequels to a parent series;
  related series appear on both detail pages.
- **Duplicate detection** — exact, typo-fuzzy (Levenshtein), and
  translated-alias (token overlap, e.g. "Akatsuki no Yona" vs "Yona of the
  Dawn") warnings before saving; batch imports can skip/update/create.
- **Bulk input** — batch **JSON API** (`POST /api/series/batch`: one
  request, one transaction for up to 1000 entries, dry-run supported) and a
  **CSV/JSON import page** (`/import`, paste or upload, dry-run supported).
- **Export/backup** — one-click **JSON** and **CSV** exports; both
  round-trip back through import.
- **Mobile-friendly** — responsive layout from phone to desktop.
- Dark/light theme toggle.
