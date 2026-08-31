#!/usr/bin/env bash
# smoke_test.sh — exercise every HTTP route once, against a freshly-spawned
# server with a clean DB. Run from the fic_tally project root after
# `go build -o fic-tally`. Exits non-zero on any unexpected status code.
#
# Usage:
#   ./fic-tally -addr 127.0.0.1:4242 &   # (started elsewhere)
#   scripts/smoke_test.sh
# Or:
#   scripts/smoke_test.sh /path/to/fic-tally   # spawns server itself
#
# ⚠ DO NOT pipe producers into `grep -q` in this script. `set -o pipefail`
# is active and grep -q exits at the first match; a still-writing producer
# (curl streaming a page) then dies with SIGPIPE (141), which pipefail turns
# into a spurious FAIL "body missing X" even though the match SUCCEEDED.
# Instead: capture into a variable and use a herestring —
#   BODY=$(curl ...) ; grep -q "needle" <<< "$BODY"
# (bash writes herestrings to a fully-buffered temp file: no pipe, no race).
# The same applies to `cmd | grep ... | grep -q` chains — only the final grep
# may lack -q, and its output must go to /dev/null.

set -euo pipefail

BIN="${1:-./fic-tally}"
ADDR="127.0.0.1:4242"
BASE="http://${ADDR}"

# --- Spawn server if not already running ---
if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/")" != "200" ]; then
  echo "spawning server: $BIN -addr $ADDR"
  rm -f fic-tally.db fic-tally.db-*
  nohup "$BIN" -addr "$ADDR" > /tmp/fic_tally_smoke.log 2>&1 &
  FT_PID=$!
  trap "kill $FT_PID 2>/dev/null || true; wait $FT_PID 2>/dev/null || true" EXIT
  sleep 1.5
else
  FT_PID=""
  trap - EXIT
fi

# Helper: assert HTTP code matches expected.
assert_code() {
  local method="$1"; local path="$2"; local expect="$3"; local data="${4:-}"
  local got
  if [ -n "$data" ]; then
    got=$(curl -s -o /dev/null -w '%{http_code}' -X "$method" "$BASE$path" --data "$data" -H 'Content-Type: application/x-www-form-urlencoded')
  else
    got=$(curl -s -o /dev/null -w '%{http_code}' -X "$method" "$BASE$path")
  fi
  if [ "$got" != "$expect" ]; then
    echo "FAIL: $method $path → expected $expect, got $got"
    exit 1
  fi
  echo "ok: $method $path → $got"
}

# Helper: assert body contains substring.
assert_body() {
  local path="$1"; local needle="$2"; local desc="${3:-}"
  local body
  body=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE$path")
  if ! grep -q -- "$needle" <<< "$body"; then
    echo "FAIL: GET $path — body missing '$needle'${desc:+ ($desc)}"
    exit 1
  fi
  echo "ok: GET $path contains '$needle'"
}

echo "── 1. Library renders + seed + rename ──"
assert_code GET / 200
assert_body / "Iron Tide"
assert_body / "Moonlit Cartographer"
assert_body / "Fic Tally"        # app renamed from Tsundoku
if grep -q "Tsundoku" <<< "$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/")"; then
  echo "FAIL: old name 'Tsundoku' still present on library page"
  exit 1
fi
echo "ok: no 'Tsundoku' anywhere on library page"

echo ""
echo "── 2. Detail renders + spec priority ──"
assert_body "/series/iron-tide" "Continue reading"
assert_body "/series/iron-tide" "Chapter 143"     # bookmark_label
assert_body "/series/iron-tide" "210&#43;"        # total_is_known=false → 210+ (escaped)
assert_body "/series/iron-tide" "Iron Tide"
assert_body "/series/iron-tide" "Isekai"          # tag link
assert_body "/series/iron-tide" "Notes / review"  # notes field present on detail
assert_body "/series/iron-tide" "Also known as"   # alt-titles line
assert_body "/series/iron-tide" "Tide of Iron"    # seed alt title
assert_body "/series/iron-tide" "Ongoing (2019)"  # pub status + year in byline

echo ""
echo "── 3. Library filters ──"
assert_body "/?status=reading" "Iron Tide"
assert_body "/?status=completed" "No series match"   # filtered empty state (no completed seeds yet)
assert_body "/?status=completed" "Clear filters"     # ...and it offers to clear the filters
assert_body "/?q=iron" "Iron Tide"
if grep -q "Moonlit" <<< "$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/?q=iron")"; then
  echo "FAIL: ?q=iron returned Moonlit (shouldn't)"
  exit 1
fi
echo "ok: ?q=iron correctly excludes Moonlit"
assert_body "/?sort=title" "Iron Tide"

echo ""
echo "── 4. Add series (POST /series/new) ──"
ADD_BODY="title=Test+Series&type=manga&author=Test+Author&description=A+test.&tags=Test,Demo&total_chapters=50&total_is_known=on&status=plan+to+read&chapter_num=&chapter_label=&rating=&notes=&bookmark_url=&bookmark_label=&cover_url=&parent_id="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "$ADD_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: POST /series/new → expected 303, got $GOT"; exit 1; }
echo "ok: POST /series/new → 303"
assert_body "/series/test-series" "Test Series"
assert_body "/series/test-series" "50"

echo ""
echo "── 5. Edit metadata (POST /series/test-series/edit) ──"
EDIT_BODY="title=Test+Series+Renamed&type=manhwa&author=Test+Author+II&description=A+renamed+test.&tags=Test,Demo,Renamed&total_chapters=60&total_is_known=on&source_url=&cover_url=&parent_id="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/edit" --data "$EDIT_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: edit → $GOT"; exit 1; }
echo "ok: POST edit → 303"
assert_body "/series/test-series" "Test Series Renamed"

echo ""
echo "── 6. Update entry (status/rating/notes) (POST /series/test-series/entry) ──"
ENTRY_BODY="status=reading&rating=7&notes=Liked+it.&bookmark_label=Chapter+10&bookmark_url=https://example.com/ch10"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/entry" --data "$ENTRY_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: entry → $GOT"; exit 1; }
echo "ok: POST entry → 303"
assert_body "/series/test-series" "Liked it"
assert_body "/series/test-series" "Chapter 10"
assert_body "/series/test-series" "dot-reading"

echo ""
echo "── 7. Progress: +1 chapter (POST /series/test-series/progress btn_plus) ──"
PROG_BODY="btn_plus=1&chapter_set=0&chapter_label="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/progress" --data "$PROG_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: +1 → $GOT"; exit 1; }
echo "ok: POST progress +1 → 303"
BODY=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/series/test-series")
if ! grep -qE 'name="chapter_set"[^>]*value="1"' <<< "$BODY"; then
  echo "FAIL: chapter not advanced to 1 after +1"
  exit 1
fi
echo "ok: chapter advanced to 1 after +1"

echo ""
echo "── 8. Progress: set chapter explicitly ──"
PROG_BODY="btn_set=1&chapter_set=15.5&chapter_label="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/progress" --data "$PROG_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: set → $GOT"; exit 1; }
echo "ok: POST progress set → 303"
BODY=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/series/test-series")
if ! grep -qE 'name="chapter_set"[^>]*value="15.5"' <<< "$BODY"; then
  echo "FAIL: chapter not set to 15.5"
  exit 1
fi
echo "ok: chapter set to 15.5"

echo ""
echo "── 9. Cover upload (POST /series/test-series/cover multipart) ──"
PNG_B64="iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB2C0AAAAASUVORK5CYII="
echo "$PNG_B64" | base64 -d > /tmp/test_cover.png
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/cover" -F "cover=@/tmp/test_cover.png")
[ "$GOT" = "303" ] || { echo "FAIL: cover upload → $GOT"; exit 1; }
echo "ok: POST cover upload → 303"
BODY=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/series/test-series/edit")
if ! grep -q '/static/covers/test-series.png' <<< "$BODY"; then
  echo "FAIL: cover not stored under /static/covers/test-series.png"
  exit 1
fi
echo "ok: cover stored at /static/covers/test-series.png"
assert_code GET /static/covers/test-series.png 200

echo ""
echo "── 10. Cover by URL (POST /series/iron-tide/cover/url) ──"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/iron-tide/cover/url" --data "cover_url=https://example.com/covers/iron.jpg" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: cover url → $GOT"; exit 1; }
echo "ok: POST cover/url → 303"
if ! grep -q 'src="https://example.com/covers/iron.jpg"' <<< "$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/series/iron-tide")"; then
  echo "FAIL: remote cover URL not rendered on detail page"
  exit 1
fi
echo "ok: remote cover URL rendered on detail page"
# invalid scheme rejected
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/iron-tide/cover/url" --data "cover_url=javascript:alert(1)" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "400" ] || { echo "FAIL: javascript: cover URL should 400, got $GOT"; exit 1; }
echo "ok: javascript: cover URL rejected with 400"
# clear it again for later visual checks
curl -s -o /dev/null -X POST "$BASE/series/iron-tide/cover/delete"

echo ""
echo "── 11. Cover delete (POST /series/test-series/cover/delete) ──"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/cover/delete")
[ "$GOT" = "303" ] || { echo "FAIL: cover delete → $GOT"; exit 1; }
echo "ok: POST cover delete → 303"
if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/static/covers/test-series.png")" = "200" ]; then
  echo "FAIL: cover file still accessible after delete"
  exit 1
fi
echo "ok: cover file removed"

echo ""
echo "── 12. Duplicate detection on add form ──"
DUP_BODY="title=Iorn+Tide&type=manga&status=plan+to+read&cover_url=&parent_id="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "$DUP_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "200" ] || { echo "FAIL: near-duplicate add should re-render warning (200), got $GOT"; exit 1; }
BODY=$(curl -s -X POST "$BASE/series/new" --data "$DUP_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
if ! grep -q "dup-warning" <<< "$BODY"; then
  echo "FAIL: dup warning box not shown for 'Iorn Tide'"
  exit 1
fi
if ! grep -q "Save anyway" <<< "$BODY"; then
  echo "FAIL: 'Save anyway' button missing"
  exit 1
fi
echo "ok: fuzzy duplicate 'Iorn Tide' triggers warning with Save-anyway"
# confirm path creates it
DUP_CONFIRM_BODY="title=Iorn+Tide&type=manga&status=plan+to+read&cover_url=&parent_id=&dup_confirm=1"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "$DUP_CONFIRM_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: confirmed dup add → expected 303, got $GOT"; exit 1; }
echo "ok: dup_confirm=1 saves anyway (303)"
# translated-alias check: token overlap
ALIAS_BODY="title=Yona+of+the+Dawn&type=manga&status=plan+to+read&cover_url=&parent_id="
# (no seed named Akatsuki no Yona — plant one first via API, tested in group 15;
#  here we just verify a clean title does NOT warn)
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "title=Something+Utterly+Unique&type=manga&status=plan+to+read&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: unique title should save directly (303), got $GOT"; exit 1; }
echo "ok: unique title saves without warning"
# cleanup the two fixtures
curl -s -o /dev/null -X POST "$BASE/series/iorn-tide/delete"
curl -s -o /dev/null -X POST "$BASE/series/something-utterly-unique/delete"

echo ""
echo "── 13. Series grouping (parent_id + related section) ──"
GROUP_BODY="title=Moonlit+Cartographer&type=web+novel&author=R.+Solace&description=D.&tags=&total_chapters=140&total_is_known=on&source_url=&cover_url=&parent_id=iron-tide&alt_titles=&pub_status=completed&year=2021&created_at=2026-01-01T00:00:00Z"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/moonlit-cartographer/edit" --data "$GROUP_BODY" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: set parent → $GOT"; exit 1; }
echo "ok: POST edit with parent_id → 303"
assert_body "/series/moonlit-cartographer" "Related series"
assert_body "/series/moonlit-cartographer" "Iron Tide"      # parent chip
assert_body "/series/iron-tide" "Moonlit Cartographer"      # child chip on parent's page
# self-parent rejected
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/iron-tide/edit" --data "title=Iron+Tide&type=manhwa&author=J.+Wren&description=D.&tags=&total_chapters=210&total_is_known=&source_url=&cover_url=&parent_id=iron-tide" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "400" ] || { echo "FAIL: self-parent should 400, got $GOT"; exit 1; }
echo "ok: self-parenting rejected with 400"
# restore moonlit to standalone
curl -s -o /dev/null -X POST "$BASE/series/moonlit-cartographer/edit" --data "title=Moonlit+Cartographer&type=web+novel&author=R.+Solace&description=D.&tags=&total_chapters=140&total_is_known=on&source_url=&cover_url=&parent_id=&alt_titles=&pub_status=completed&year=2021" -H 'Content-Type: application/x-www-form-urlencoded'

echo ""
echo "── 14. Delete series (POST /series/test-series/delete) ──"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/test-series/delete")
[ "$GOT" = "303" ] || { echo "FAIL: delete → $GOT"; exit 1; }
echo "ok: POST delete → 303"
assert_code GET /series/test-series 404

echo ""
echo "── 15. Stats page (GET /stats) ──"
assert_code GET /stats 200
assert_body /stats "Reading stats"
assert_body /stats "Currently reading"
assert_body /stats "Average rating"
assert_body /stats "Reading streak"
# seeds: 2 reading, ratings 8+7 → avg 7.5
BODY=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/stats")
if ! grep -A3 "Currently reading" <<< "$BODY" | grep '>2<' > /dev/null; then
  echo "FAIL: 'Currently reading' should be 2 (seeds)"
  exit 1
fi
echo "ok: currently reading = 2"
if ! grep -q '7\.5' <<< "$BODY"; then
  echo "FAIL: average rating 7.5 not shown"
  exit 1
fi
echo "ok: average rating 7.5"
assert_body /stats "Reading activity"   # 30-day strip

echo ""
echo "── 16. Streak + completed-this-month via progress/status ──"
# advance a chapter → daily_reads logs today → streak = 1
curl -s -o /dev/null -X POST "$BASE/series/iron-tide/progress" --data "btn_plus=1&chapter_set=0&chapter_label=" -H 'Content-Type: application/x-www-form-urlencoded'
assert_body /stats "longest: 1d"
# complete moonlit → completed_at set → "Completed this month" = 1
curl -s -o /dev/null -X POST "$BASE/series/moonlit-cartographer/entry" --data "status=completed&rating=7&notes=&bookmark_label=&bookmark_url=" -H 'Content-Type: application/x-www-form-urlencoded'
BODY=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/stats")
if ! grep -A3 "Completed this month" <<< "$BODY" | grep '>1<' > /dev/null; then
  echo "FAIL: 'Completed this month' should be 1 after completing a seed"
  exit 1
fi
echo "ok: completed this month = 1"
assert_body /stats "Recently completed"
# revert moonlit to reading (also exercises completed_at clearing)
curl -s -o /dev/null -X POST "$BASE/series/moonlit-cartographer/entry" --data "status=reading&rating=7&notes=&bookmark_label=Chapter+89&bookmark_url=" -H 'Content-Type: application/x-www-form-urlencoded'
BODY=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/stats")
if grep -A3 "Completed this month" <<< "$BODY" | grep '>1<' > /dev/null; then
  echo "FAIL: 'Completed this month' should be back to 0 after reverting status"
  exit 1
fi
echo "ok: completed counter reverts when status leaves completed"

echo ""
echo "── 17. Batch JSON API (POST /api/series/batch) ──"
cat > /tmp/batch1.json <<'EOF'
{
  "series": [
    {"title": "Akatsuki no Yona", "type": "manga", "status": "reading",
     "current_chapter_number": 120, "rating": 9, "tags": ["Fantasy", "Shoujo"]},
    {"title": "Iron Tide", "type": "manhwa", "status": "reading", "rating": 8}
  ],
  "duplicate_policy": "skip"
}
EOF
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data @/tmp/batch1.json)
if ! grep -q '"created": 1' <<< "$RESP"; then
  echo "FAIL: batch API expected 1 created, got: $RESP"
  exit 1
fi
if ! grep -q '"skipped": 1' <<< "$RESP"; then
  echo "FAIL: batch API expected 1 skipped (Iron Tide exact dup), got: $RESP"
  exit 1
fi
echo "ok: batch API created 1, skipped exact dup 1"
assert_body "/series/akatsuki-no-yona" "Akatsuki no Yona"
# translated alias fuzzy note: "Yona of the Dawn" vs "Akatsuki no Yona"
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series":[{"title":"Yona of the Dawn","type":"manga","status":"plan to read"}],"duplicate_policy":"skip"}')
if ! grep -qi 'shares key word' <<< "$RESP"; then
  echo "FAIL: translated-alias fuzzy note missing, got: $RESP"
  exit 1
fi
if ! grep -q '"created": 1' <<< "$RESP"; then
  echo "FAIL: fuzzy match should NOT block creation, got: $RESP"
  exit 1
fi
echo "ok: translated alias flagged but still created"
# dry run: nothing persisted
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series":[{"title":"Dry Run Probe","type":"manga"}],"dry_run":true}')
if ! grep -q '"dry_run": true' <<< "$RESP"; then
  echo "FAIL: dry_run not echoed, got: $RESP"
  exit 1
fi
if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/series/dry-run-probe")" = "200" ]; then
  echo "FAIL: dry run persisted a row"
  exit 1
fi
echo "ok: dry run persisted nothing"
# invalid JSON → 400
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series": [ {broken')
[ "$GOT" = "400" ] || { echo "FAIL: malformed JSON should 400, got $GOT"; exit 1; }
echo "ok: malformed JSON → 400"
# bare array form
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '[{"title":"Bare Array Form","type":"manga"}]')
if ! grep -q '"created": 1' <<< "$RESP"; then
  echo "FAIL: bare-array batch failed, got: $RESP"
  exit 1
fi
echo "ok: bare-array request accepted"
# per-item validation error doesn't kill the batch
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series":[{"title":"Valid One","type":"manga"},{"title":"","type":"manga"},{"title":"Bad Rating","type":"manga","rating":99}]}')
if ! grep -q '"failed": 2' <<< "$RESP"; then
  echo "FAIL: expected 2 per-item failures, got: $RESP"
  exit 1
fi
echo "ok: per-item validation errors reported, batch continues"
# cleanup API fixtures
for id in akatsuki-no-yona yona-of-the-dawn bare-array-form valid-one; do
  curl -s -o /dev/null -X POST "$BASE/series/$id/delete"
done

echo ""
echo "── 18. CSV import (POST /import) ──"
cat > /tmp/import1.csv <<'EOF'
title,type,author,status,chapter_num,rating,tags,total_chapters,total_is_known
Iron Tide,manhwa,J. Wren,reading,142,8,"Isekai, Naval",210,false
Cradle,light novel,Will Wight,completed,12,8,Progression|Xianxia,12,true
EOF
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/import" --data-urlencode "payload@/tmp/import1.csv" --data "dup_policy=skip")
[ "$GOT" = "200" ] || { echo "FAIL: CSV import → $GOT"; exit 1; }
BODY=$(curl -s -X POST "$BASE/import" --data-urlencode "payload@/tmp/import1.csv" --data "dup_policy=skip")
if ! grep -q "skipped" <<< "$BODY"; then
  echo "FAIL: CSV import should skip exact dup Iron Tide"
  exit 1
fi
if ! grep -q "Cradle" <<< "$BODY"; then
  echo "FAIL: CSV import result table missing Cradle"
  exit 1
fi
echo "ok: CSV import skips dup + reports per-row results"
assert_body "/series/cradle" "Will Wight"
assert_body "/series/cradle" "12"          # total chapters (known → no +)
# bad CSV (no title column) → error box
printf 'type,status\nmanga,reading\n' > /tmp/bad.csv
BODY=$(curl -s -X POST "$BASE/import" --data-urlencode "payload@/tmp/bad.csv" --data "dup_policy=skip")
if ! grep -q "import-error" <<< "$BODY"; then
  echo "FAIL: headerless CSV should show error box"
  exit 1
fi
echo "ok: CSV without title column rejected"
# dry run writes nothing
printf 'title,type\nDry Run CSV,manga\n' > /tmp/dry.csv
curl -s -o /dev/null -X POST "$BASE/import" --data-urlencode "payload@/tmp/dry.csv" --data "dup_policy=skip&dry_run=on"
if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/series/dry-run-csv")" = "200" ]; then
  echo "FAIL: CSV dry run persisted a row"
  exit 1
fi
echo "ok: CSV dry run persisted nothing"

echo ""
echo "── 19. JSON import via file upload (multipart) ──"
curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/export/json" > /tmp/export.json
if ! grep -q "Iron Tide" /tmp/export.json; then
  echo "FAIL: JSON export missing seed"
  exit 1
fi
echo "ok: GET /export/json contains library"
# re-import the export with policy=update → every row matches, all updated
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/import" -F "file=@/tmp/export.json" -F "dup_policy=update")
[ "$GOT" = "200" ] || { echo "FAIL: JSON file import → $GOT"; exit 1; }
BODY=$(curl -s -X POST "$BASE/import" -F "file=@/tmp/export.json" -F "dup_policy=update")
UPDATED_N=$(echo "$BODY" | grep -oE '<strong>[0-9]+</strong> updated' | grep -oE '[0-9]+' | head -1)
TOTAL=$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/export/json" | grep -c '"title"')
if [ "${UPDATED_N:-0}" -lt "$TOTAL" ]; then
  echo "FAIL: re-import with policy=update should update all $TOTAL rows, reported $UPDATED_N"
  exit 1
fi
echo "ok: export → import round-trip updates all rows ($UPDATED_N)"

echo ""
echo "── 20. CSV export ──"
curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/export/csv" > /tmp/export.csv
if ! grep -q '^title,' <<< "$(head -1 /tmp/export.csv)"; then
  echo "FAIL: CSV export header wrong: $(head -1 /tmp/export.csv)"
  exit 1
fi
if ! grep -q "Iron Tide" /tmp/export.csv; then
  echo "FAIL: CSV export missing seed row"
  exit 1
fi
echo "ok: GET /export/csv has canonical header + rows"

echo ""
echo "── 21. Import page renders ──"
assert_code GET /import 200
assert_body /import "Batch import"
assert_body /import "Dry run"

echo ""
echo "── 22. Theme toggle (POST /theme) ──"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/theme" --data "theme=light&back=/")
[ "$GOT" = "303" ] || { echo "FAIL: theme → $GOT"; exit 1; }
echo "ok: POST /theme → 303"
# Theme is a SERVER-side setting now (no cookie): a cookie-less request —
# simulating a different browser — must render the chosen theme too.
if ! grep -q 'data-theme="light"' <<< "$(curl -s "$BASE/")"; then
  echo "FAIL: theme setting not applied to rendered page (expected data-theme=\"light\")"
  exit 1
fi
if grep -qi '^set-cookie:' <<< "$(curl -s -D - -o /dev/null -X POST "$BASE/theme" --data "theme=light&back=/")"; then
  echo "FAIL: POST /theme still sets a cookie (theme is server-side now)"
  exit 1
fi
echo "ok: theme stored server-side, applied for any browser"

echo ""
echo "── 23. 404 handling ──"
assert_code GET /series/nonexistent 404
assert_code GET /no/such/path 404

echo ""
echo "── 24. Static assets ──"
assert_code GET /static/css/app.css 200
assert_code GET /static/js/app.js 200

echo ""
echo "── 25. Alternative titles + pub status + year (add / search / edit) ──"
# add with new fields (alt_titles newline-separated, URL-encoded %0A)
ADD_NEW="title=Twilight+Blossom&alt_titles=Akatsuki+no+Hana%0ANight+Bloom&pub_status=hiatus&year=2018&type=manga&author=T.+Sakura&description=D.&tags=&total_chapters=&total_is_known=&status=plan+to+read&cover_url=&parent_id="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "$ADD_NEW" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: add with new fields → $GOT"; exit 1; }
echo "ok: POST /series/new with alt_titles/pub_status/year → 303"
assert_body "/series/twilight-blossom" "Also known as"
assert_body "/series/twilight-blossom" "Akatsuki no Hana"
assert_body "/series/twilight-blossom" "Night Bloom"
assert_body "/series/twilight-blossom" "Hiatus"
assert_body "/series/twilight-blossom" "2018"
# search by alternative title (both of them)
assert_body "/?q=akatsuki+no+hana" "Twilight Blossom"
assert_body "/?q=night+bloom" "Twilight Blossom"
if grep -q "Iron Tide" <<< "$(curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/?q=night+bloom")"; then
  echo "FAIL: ?q=night+bloom should only match Twilight Blossom"
  exit 1
fi
echo "ok: search finds series via its alternative titles"
# edit form preserves + updates the new fields
EDIT_NEW="title=Twilight+Blossom&alt_titles=Akatsuki+no+Hana%0ANight+Bloom+Rebirth&pub_status=cancelled&year=2017&type=manga&author=T.+Sakura&description=D.&tags=&total_chapters=&total_is_known=&source_url=&cover_url=&parent_id="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/twilight-blossom/edit" --data "$EDIT_NEW" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: edit new fields → $GOT"; exit 1; }
assert_body "/series/twilight-blossom" "Night Bloom Rebirth"
assert_body "/series/twilight-blossom" "Canceled"
assert_body "/series/twilight-blossom" "2017"
echo "ok: edit updates alt_titles / pub_status / year"
# validation: bad pub_status → 400
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "title=Bad+Pub&type=manga&pub_status=finished&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "400" ] || { echo "FAIL: bad pub_status should 400, got $GOT"; exit 1; }
echo "ok: unknown pub_status rejected with 400"
# validation: bad year → 400
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "title=Bad+Year&type=manga&year=12345&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "400" ] || { echo "FAIL: bad year should 400, got $GOT"; exit 1; }
echo "ok: out-of-range year rejected with 400"


echo ""
echo "── 26. Duplicate detection via alternative titles ──"
# incoming TITLE matches a stored ALT title → strong warning
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "title=Akatsuki+no+Hana&type=manga&status=plan+to+read&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "200" ] || { echo "FAIL: alt-title dup should re-render warning (200), got $GOT"; exit 1; }
BODY=$(curl -s -X POST "$BASE/series/new" --data "title=Akatsuki+no+Hana&type=manga&status=plan+to+read&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
if ! grep -q "dup-warning" <<< "$BODY"; then
  echo "FAIL: alt-title dup warning box missing"
  exit 1
fi
if ! grep -q 'matches alternative title' <<< "$BODY"; then
  echo "FAIL: alt-title match reason missing: $BODY"
  exit 1
fi
echo "ok: incoming title vs stored alt title triggers strong warning"
# incoming ALT title matches a stored main title → strong warning too
BODY=$(curl -s -X POST "$BASE/series/new" --data "title=Some+Fresh+Unique+Name&type=manga&alt_titles=Twilight+Blossom&status=plan+to+read&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
if ! grep -q "dup-warning" <<< "$BODY"; then
  echo "FAIL: incoming alt-title dup warning missing"
  exit 1
fi
echo "ok: incoming alt title vs stored main title triggers warning"
# batch API: title equal to a stored alt title counts as exact dup (policy=skip)
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series":[{"title":"Akatsuki no Hana","type":"manga"}],"duplicate_policy":"skip"}')
if ! grep -q '"skipped": 1' <<< "$RESP"; then
  echo "FAIL: batch API should skip alt-title duplicate, got: $RESP"
  exit 1
fi
echo "ok: batch API treats alt-title equality as an exact duplicate"


echo ""
echo "── 27. New fields through import / export / batch API ──"
# CSV export: new columns present, seed + fixture rows carry values
curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/export/csv" > /tmp/export27.csv
if ! grep -q '^title,alt_titles,type,author,year,pub_status,' <<< "$(head -1 /tmp/export27.csv)"; then
  echo "FAIL: CSV export header missing new columns: $(head -1 /tmp/export27.csv)"
  exit 1
fi
if ! grep -q 'Twilight Blossom' /tmp/export27.csv || ! grep -q 'Akatsuki no Hana' /tmp/export27.csv; then
  echo "FAIL: CSV export missing alt-titles value"
  exit 1
fi
if ! grep -q 'cancelled' /tmp/export27.csv || ! grep -q '2017' /tmp/export27.csv; then
  echo "FAIL: CSV export missing pub_status/year values"
  exit 1
fi
echo "ok: CSV export carries alt_titles / pub_status / year"
# JSON export: array + object fields present
curl -s --retry 3 --retry-connrefused --retry-delay 1 "$BASE/export/json" > /tmp/export27.json
if ! grep -q '"alt_titles"' /tmp/export27.json || ! grep -q '"pub_status"' /tmp/export27.json || ! grep -q '"year"' /tmp/export27.json; then
  echo "FAIL: JSON export missing new fields"
  exit 1
fi
echo "ok: JSON export carries alt_titles / pub_status / year"
# CSV import with the new columns
printf 'title,alt_titles,type,author,year,pub_status,status\nFrostlight Saga,"Frost Saga; Saga of Frost",manga,A. Winter,2020,ongoing,reading\n' > /tmp/import27.csv
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/import" --data-urlencode "payload@/tmp/import27.csv" --data "dup_policy=skip")
[ "$GOT" = "200" ] || { echo "FAIL: CSV import with new columns → $GOT"; exit 1; }
assert_body "/series/frostlight-saga" "Frost Saga"
assert_body "/series/frostlight-saga" "Saga of Frost"
assert_body "/series/frostlight-saga" "Ongoing"
assert_body "/series/frostlight-saga" "2020"
echo "ok: CSV import parses alt_titles ( ;-separated) / pub_status / year"
# JSON import with alt_titles as array AND as a plain string
printf '{"series":[{"title":"Mirror Realm","alt_titles":["Kagami no Sekai","Mirrorland"],"type":"manga","year":2015,"pub_status":"completed","status":"completed"},{"title":"Ashen Crown","alt_titles":"Shikkoku no Oukan; Crown of Ash","type":"manhwa","year":2022,"pub_status":"ongoing"}]}' > /tmp/import27.json
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/import" --data-urlencode "payload@/tmp/import27.json" --data "dup_policy=skip")
[ "$GOT" = "200" ] || { echo "FAIL: JSON import with new fields → $GOT"; exit 1; }
assert_body "/series/mirror-realm" "Kagami no Sekai"
assert_body "/series/mirror-realm" "Completed"
assert_body "/series/mirror-realm" "2015"
assert_body "/series/ashen-crown" "Crown of Ash"
assert_body "/series/ashen-crown" "Ongoing"
echo "ok: JSON import accepts alt_titles as array or string"
# batch API validation: bad pub_status / bad year fail per-item
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series":[{"title":"BadPub API","type":"manga","pub_status":"finished"},{"title":"BadYear API","type":"manga","year":12345},{"title":"GoodRow API","type":"manga","year":1999,"pub_status":"hiatus"}]}')
if ! grep -q '"failed": 2' <<< "$RESP"; then
  echo "FAIL: expected 2 API validation failures, got: $RESP"
  exit 1
fi
echo "ok: API rejects bad pub_status / year without killing the batch"
assert_body "/series/goodrow-api" "Hiatus"
assert_body "/series/goodrow-api" "1999"
# cleanup group fixtures
for id in twilight-blossom frostlight-saga mirror-realm ashen-crown goodrow-api; do
  curl -s -o /dev/null -X POST "$BASE/series/$id/delete"
done


echo ""
echo "── 28. Stats: publication-status breakdown ──"
assert_body /stats "By publication status"
# seeds carry pub statuses (iron-tide ongoing, moonlit completed)
assert_body /stats "Ongoing"
assert_body /stats "Completed"
echo "ok: stats page shows publication breakdown"


echo ""
echo "── 29. Button-driven search + layouts + ribbon config + completion emblem ──"
# 29a. Search is button-driven: a Search submit button exists, the clear
#      button only appears when a query is active, and app.js no longer
#      auto-submits on keystroke (the old per-keystroke behavior slowed the
#      page down on large libraries).
assert_body / ">Search</button>"
assert_body "/?q=iron" 'id="search-clear"'
LIBBODY=$(curl -s "$BASE/")
if grep -q 'id="search-clear"' <<< "$LIBBODY"; then
  echo "FAIL: clear button shown without an active query"
  exit 1
fi
echo "ok: search submits via button; clear button only with active query"
JSBODY=$(curl -s "$BASE/static/js/app.js")
if grep -q "search.addEventListener('input'" <<< "$JSBODY"; then
  echo "FAIL: app.js still wires live per-keystroke search"
  exit 1
fi
echo "ok: app.js has no per-keystroke auto-submit"

# 29b. Layout switch (default / compact / details) + preference pre-paint
assert_body / 'data-layout="default"'
assert_body / 'data-layout="compact"'
assert_body / 'data-layout="details"'
assert_body / "fic-tally:layout"          # legacy pref fallback in layout.html pre-paint
CSSBODY=$(curl -s "$BASE/static/css/app.css")
for RULE in 'html[data-layout="compact"]' 'html[data-layout="details"]' 'card-extra'; do
  if ! grep -qF -- "$RULE" <<< "$CSSBODY"; then
    echo "FAIL: app.css missing layout rule '$RULE'"
    exit 1
  fi
done
echo "ok: layout switch + compact/details CSS present"
assert_body / "card-facts"                # details-layout extra fields in DOM

# 29c. Bookmark-style popover: color, transparency, width, shape, side
assert_body / 'id="bm-panel"'
assert_body / 'id="bm-color"'
assert_body / 'id="bm-opacity"'
assert_body / 'id="bm-width"'
assert_body / 'data-shape="triangle"'
assert_body / 'data-side="right"'
assert_body / 'id="bm-reset"'
for RULE in '--bm-width' '--bm-opacity' 'data-ribbon-shape' 'data-ribbon-side'; do
  if ! grep -qF -- "$RULE" <<< "$CSSBODY"; then
    echo "FAIL: app.css missing ribbon-config rule '$RULE'"
    exit 1
  fi
done
echo "ok: ribbon customization popover + CSS custom properties present"

# 29d. Completion emblem: gold seal only when reading status AND publication
#      status are both "completed".
cat > /tmp/batch_emblem.json <<'EOF'
{
  "series": [
    {"title": "Fully Finished Saga", "type": "manga", "pub_status": "completed",
     "year": 2015, "total_chapters": 96, "total_is_known": true,
     "status": "completed", "current_chapter_number": 96, "rating": 9},
    {"title": "Read But Ongoing", "type": "manga", "pub_status": "ongoing",
     "year": 2023, "total_chapters": 40,
     "status": "completed", "current_chapter_number": 40},
    {"title": "Unread But Complete", "type": "manhwa", "pub_status": "completed",
     "year": 2012, "total_chapters": 72,
     "status": "reading", "current_chapter_number": 5}
  ]
}
EOF
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data @/tmp/batch_emblem.json)
if ! grep -q '"created": 3' <<< "$RESP"; then
  echo "FAIL: emblem seed batch expected 3 created, got: $RESP"
  exit 1
fi
assert_body / "emblem-complete"                   # at least one emblem on the grid
assert_body /series/fully-finished-saga "emblem-complete"
for ID in read-but-ongoing unread-but-complete; do
  DETBODY=$(curl -s "$BASE/series/$ID")
  case "$ID" in
    read-but-ongoing)   DETTITLE="Read But Ongoing" ;;
    unread-but-complete) DETTITLE="Unread But Complete" ;;
  esac
  if ! grep -q "$DETTITLE" <<< "$DETBODY"; then
    echo "FAIL: detail page for $ID did not render (title missing)"
    exit 1
  fi
  if grep -q "emblem-complete" <<< "$DETBODY"; then
    echo "FAIL: emblem shown on $ID (should need BOTH statuses completed)"
    exit 1
  fi
done
echo "ok: completion emblem only for reading+publication both completed"


echo ""
echo "── 30. Asset cache-busting (stale-CSS/JS upgrade breakage) ──"
# A user upgraded the binary+templates but their browser kept serving the
# old app.js/app.css from cache (no Cache-Control header → heuristic
# caching): new HTML + old assets = unstyled cards, dead buttons, and the
# old live per-keystroke search. Two defenses:
#   a) every response carries Cache-Control: no-cache → browser must
#      revalidate before reusing a cached copy (304s keep it cheap)
#   b) asset URLs carry ?v= → a changed asset gets a fresh URL, which can
#      never match an already-cached entry (rescues stale caches without
#      requiring a hard refresh)
# 30a. no-cache on static assets AND on rendered pages
for PATH_ in /static/css/app.css /static/js/app.js /; do
  HDRS=$(curl -s -D - -o /dev/null "$BASE$PATH_")
  if ! grep -qi '^cache-control: *no-cache' <<< "$HDRS"; then
    echo "FAIL: $PATH_ missing Cache-Control: no-cache"
    exit 1
  fi
done
echo "ok: Cache-Control: no-cache on assets and pages"
# 30b. revalidation still works: an If-Modified-Since newer than the file's
#      mtime yields 304 (no-cache must not disable caching entirely)
NOW_HTTP=$(date -u '+%a, %d %b %Y %H:%M:%S GMT' -d '+1 hour')
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "If-Modified-Since: $NOW_HTTP" "$BASE/static/css/app.css")
if [ "$CODE" != "304" ]; then
  echo "FAIL: expected 304 on revalidation, got $CODE"
  exit 1
fi
echo "ok: revalidation returns 304 (cheap no-cache)"
# 30c. versioned asset URLs in the served HTML
assert_body / "app.css?v="
assert_body / "app.js?v="
echo "ok: HTML references versioned asset URLs"

echo ""
echo "── 31. Configurable completion emblem ──"
# The completion emblem (reading + publication both completed) is
# client-configurable: show/hide, style (seal/check/star), color, size,
# transparency, corner. Same pattern as the ribbon settings: the span is
# always server-rendered; prefs are stored SERVER-side (settings table,
# POST /api/settings — see group 32) and applied as CSS custom properties
# + data attributes on <html>.
# 31a. Settings popover exists on the library page with all controls
assert_body / 'id="em-toggle"'
assert_body / 'id="em-panel"'
assert_body / 'data-show="off"'
assert_body / 'data-style="star"'
assert_body / 'data-style="check"'
assert_body / 'id="em-color"'
assert_body / 'id="em-size"'
assert_body / 'id="em-opacity"'
assert_body / 'data-pos="tl"'
assert_body / 'data-pos="br"'
assert_body / 'id="em-reset"'
echo "ok: emblem settings popover present"
# 31b. Emblem markup carries BOTH icons (check + star); CSS picks one
assert_body / "em-icon-check"
assert_body / "em-icon-star"
for RULE in '--em-size' '--em-color' '--em-opacity' 'data-emblem-style' 'data-emblem-pos' 'data-emblem-hidden'; do
  if ! grep -qF -- "$RULE" <<< "$CSSBODY"; then
    echo "FAIL: app.css missing emblem-config rule '$RULE'"
    exit 1
  fi
done
echo "ok: both icons rendered + emblem CSS custom properties present"
# 31c. Persistence is server-side: app.js syncs prefs via POST
#      /api/settings (localStorage keys appear only in the one-time
#      legacy migration), and the pre-paint script reads the
#      server-rendered #ft-settings blob.
if ! grep -q "'/api/settings'" <<< "$JSBODY"; then
  echo "FAIL: app.js missing /api/settings persistence"
  exit 1
fi
if ! grep -q "fic-tally:emblem" <<< "$JSBODY"; then
  echo "FAIL: app.js missing legacy fic-tally:emblem migration"
  exit 1
fi
if ! grep -q "mergeLegacyPrefs" <<< "$JSBODY"; then
  echo "FAIL: app.js missing legacy pref migration routine"
  exit 1
fi
LIBHTML=$(curl -s "$BASE/")
if ! grep -q 'id="ft-settings"' <<< "$LIBHTML"; then
  echo "FAIL: layout.html missing server-rendered #ft-settings blob"
  exit 1
fi
echo "ok: emblem prefs persisted server-side + applied pre-paint"
# 31d. Compact layout scales the emblem via the size custom property
if ! grep -qF 'html[data-layout="compact"] { --em-size:20px; }' <<< "$CSSBODY"; then
  echo "FAIL: compact layout no longer scales --em-size"
  exit 1
fi
echo "ok: compact layout scales emblem size"

echo ""
echo "── 32. Server-side UI settings (per-server, not per-browser) ──"
# Layout / ribbon / emblem / theme prefs live in the server's SQLite
# settings table: every browser renders the same look. curl carries no
# cookies and no localStorage, so every request below is effectively a
# different browser — if prefs were still per-browser, these checks fail.
# 32a. GET /api/settings returns the stored groups as JSON
RESP=$(curl -s "$BASE/api/settings")
if ! grep -q '^{' <<< "$RESP"; then
  echo "FAIL: GET /api/settings did not return JSON: $RESP"
  exit 1
fi
grep -q '"theme":"light"' <<< "$RESP" || { echo "FAIL: theme (set in group 22) missing from settings: $RESP"; exit 1; }
echo "ok: GET /api/settings returns stored groups"
# 32b. POST full settings — validated, canonicalized, echoed back
RESP=$(curl -s -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"layout":"compact","ribbon":{"color":"#5b7fbd","opacity":0.85,"width":9,"shape":"round","side":"right"},"emblem":{"show":"on","style":"star","color":"#b8bcc4","size":34,"opacity":0.7,"pos":"tl"},"theme":"dark"}')
for NEEDLE in '"layout":"compact"' '"color":"#5b7fbd"' '"width":9' '"shape":"round"' '"side":"right"' '"show":"on"' '"style":"star"' '"size":34' '"pos":"tl"' '"theme":"dark"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$RESP"; then
    echo "FAIL: POST /api/settings response missing $NEEDLE: $RESP"
    exit 1
  fi
done
echo "ok: POST /api/settings stores + echoes canonical settings"
# 32c. settings persist in the store
RESP=$(curl -s "$BASE/api/settings")
for NEEDLE in '"layout":"compact"' '"width":9' '"show":"on"' '"color":"#b8bcc4"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$RESP"; then
    echo "FAIL: GET /api/settings after save missing $NEEDLE: $RESP"
    exit 1
  fi
done
echo "ok: settings persisted in the store"
# 32d. rendered pages carry the prefs server-side (data-* attributes on
#     <html> + the #ft-settings blob) — with no cookies sent, i.e. a
#     brand-new browser gets the same look
LIBHTML=$(curl -s "$BASE/")
for NEEDLE in 'data-layout="compact"' 'data-ribbon-shape="round"' 'data-ribbon-side="right"' 'data-emblem-style="star"' 'data-emblem-pos="tl"' 'data-theme="dark"' 'id="ft-settings"' '"layout":"compact"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$LIBHTML"; then
    echo "FAIL: library page missing server-rendered pref $NEEDLE"
    exit 1
  fi
done
if ! grep -qF 'data-layout="compact"' <<< "$(curl -s "$BASE/stats")"; then
  echo "FAIL: prefs not applied on the stats page"
  exit 1
fi
echo "ok: prefs rendered server-side on every page, any browser"
# 32e. validation rejects garbage payloads (bad enums, out-of-range
#     numbers, non-hex colors, unknown groups, non-JSON) with 400
for BAD in '{"layout":"banana"}' '{"ribbon":{"color":"javascript:alert(1)"}}' '{"ribbon":{"width":99}}' '{"ribbon":{"shape":"square"}}' '{"ribbon":{"opacity":0.01}}' '{"emblem":{"style":"diamond"}}' '{"emblem":{"size":5}}' '{"emblem":{"pos":"center"}}' '{"emblem":{"show":"maybe"}}' '{"unknown":1}' 'not-json'; do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data "$BAD")
  if [ "$CODE" != "400" ]; then
    echo "FAIL: invalid settings '$BAD' → expected 400, got $CODE"
    exit 1
  fi
done
echo "ok: invalid settings rejected with 400"
# 32f. cross-origin writes rejected (drive-by POST from a malicious page)
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/settings" -H 'Content-Type: application/json' -H 'Origin: http://evil.example' --data '{"layout":"compact"}')
if [ "$CODE" != "403" ]; then
  echo "FAIL: cross-origin settings POST → expected 403, got $CODE"
  exit 1
fi
echo "ok: cross-origin settings writes blocked"
# 32g. the rejected writes didn't corrupt the stored settings
RESP=$(curl -s "$BASE/api/settings")
grep -qF '"layout":"compact"' <<< "$RESP" || { echo "FAIL: stored layout lost after invalid posts: $RESP"; exit 1; }
grep -q '"shape":"square"' <<< "$RESP" && { echo "FAIL: invalid shape was stored"; exit 1; }
echo "ok: stored settings intact after rejected writes"
# 32h. partial group updates are canonicalized with server-side defaults
RESP=$(curl -s -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"ribbon":{"color":"#a8342a"}}')
for NEEDLE in '"width":11' '"shape":"tag"' '"side":"left"' '"opacity":1'; do
  if ! grep -qF -- "$NEEDLE" <<< "$RESP"; then
    echo "FAIL: partial ribbon update missing default $NEEDLE: $RESP"
    exit 1
  fi
done
echo "ok: partial updates canonicalized with defaults"

echo ""
echo "── 33. Default sort + saved default sort ──"
# 33a. out of the box, "Last updated" is the selected sort and the
#     save-as-default star is active (current == default)
LIBHTML=$(curl -s "$BASE/")
if ! grep -qF '<option value="updated" selected>Last updated' <<< "$LIBHTML"; then
  echo "FAIL: default sort is not 'Last updated'"
  exit 1
fi
echo "ok: default sort = Last updated"
if ! grep -qF 'id="sort-default" class="sort-default-btn active"' <<< "$LIBHTML"; then
  echo "FAIL: save-as-default star not active on the default sort"
  exit 1
fi
echo "ok: star active on default"
# 33b. saving a default sort (library settings group) round-trips and is
#     applied on / for any browser (curl carries no cookies/storage)
RESP=$(curl -s -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"library":{"sort":"title"}}')
if ! grep -qF '"library":{"sort":"title"}' <<< "$RESP"; then
  echo "FAIL: POST library group not echoed: $RESP"
  exit 1
fi
echo "ok: POST library sort=title echoed"
LIBHTML=$(curl -s "$BASE/")
if ! grep -qF '<option value="title" selected>Title' <<< "$LIBHTML"; then
  echo "FAIL: saved default sort not applied on /"
  exit 1
fi
if ! grep -qF '"library":{"sort":"title"}' <<< "$LIBHTML"; then
  echo "FAIL: #ft-settings blob missing library group"
  exit 1
fi
echo "ok: saved default sort applied server-side (ft-settings blob too)"
# 33c. an explicit ?sort= always beats the saved default; garbage falls back
LIBHTML=$(curl -s "$BASE/?sort=rating")
if ! grep -qF '<option value="rating" selected>Rating' <<< "$LIBHTML"; then
  echo "FAIL: explicit ?sort=rating did not win over saved default"
  exit 1
fi
LIBHTML=$(curl -s "$BASE/?sort=bogus")
if ! grep -qF '<option value="title" selected>Title' <<< "$LIBHTML"; then
  echo "FAIL: invalid ?sort= did not fall back to the saved default"
  exit 1
fi
echo "ok: explicit sort wins; invalid sort falls back to saved default"
# 33d. validation: bad enum rejected, partial update canonicalized
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"library":{"sort":"bogus"}}')
if [ "$CODE" != "400" ]; then
  echo "FAIL: library.sort=bogus → expected 400, got $CODE"
  exit 1
fi
RESP=$(curl -s -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"library":{}}')
if ! grep -qF '"library":{"sort":"updated"}' <<< "$RESP"; then
  echo "FAIL: empty library patch not canonicalized to the default: $RESP"
  exit 1
fi
echo "ok: library group validated + canonicalized"
# 33e. reset the saved default back to the built-in one for later groups
curl -s -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"library":{"sort":"updated"}}' >/dev/null
echo "ok: saved default reset to updated"

echo ""
echo "── 34. Multi-tag filtering + #tag search ──"
# 34a. create a probe series with three tags
PROBE="title=Tag+Probe&type=manga&author=Probe&description=Probe.&tags=Alpha,Beta,Gamma&total_chapters=&total_is_known=&status=plan+to+read&cover_url=&parent_id="
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "$PROBE" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: POST tag probe → expected 303, got $GOT"; exit 1; }
echo "ok: tag probe series created"
# 34b. comma-separated ?tag= has AND semantics
BODY=$(curl -s "$BASE/?tag=Alpha,Beta")
if ! grep -q "Tag Probe" <<< "$BODY"; then
  echo "FAIL: ?tag=Alpha,Beta missing Tag Probe"
  exit 1
fi
if grep -q "Iron Tide" <<< "$BODY"; then
  echo "FAIL: ?tag=Alpha,Beta matched a series without both tags"
  exit 1
fi
echo "ok: ?tag=Alpha,Beta AND-filters"
BODY=$(curl -s "$BASE/?tag=Alpha,Delta")
if ! grep -q "No series match" <<< "$BODY"; then
  echo "FAIL: ?tag=Alpha,Delta should match nothing"
  exit 1
fi
echo "ok: ?tag=Alpha,Delta matches nothing"
# 34c. tag matching is case-insensitive
BODY=$(curl -s "$BASE/?tag=alpha,BETA")
if ! grep -q "Tag Probe" <<< "$BODY"; then
  echo "FAIL: ?tag=alpha,BETA (case) missing Tag Probe"
  exit 1
fi
echo "ok: tag matching case-insensitive"
# 34d. chips row: rendered for every active tag, each chip's link keeps
#     the other tags (removal without JavaScript)
BODY=$(curl -s "$BASE/?tag=Alpha,Beta")
for NEEDLE in 'class="tag-chips"' 'Filtering by' 'tag=Beta' 'tag=Alpha'; do
  if ! grep -qF -- "$NEEDLE" <<< "$BODY"; then
    echo "FAIL: chips row missing '$NEEDLE'"
    exit 1
  fi
done
if ! grep -qF '2 tags selected' <<< "$BODY"; then
  echo "FAIL: tag dropdown does not show the multi-tag state"
  exit 1
fi
if ! grep -qF 'mini-tag tag-active' <<< "$BODY"; then
  echo "FAIL: active mini-tag not highlighted on cards"
  exit 1
fi
echo "ok: chips + multi-state dropdown + highlighted mini-tags"
# 34e. #tag tokens in the search box filter by tag (AND), mix with text
BODY=$(curl -s --get "$BASE/" --data-urlencode "q=#Alpha #Gamma")
if ! grep -q "Tag Probe" <<< "$BODY" || grep -q "Iron Tide" <<< "$BODY"; then
  echo "FAIL: q=#Alpha #Gamma returned wrong results"
  exit 1
fi
BODY=$(curl -s --get "$BASE/" --data-urlencode "q=#Alpha probe")
if ! grep -q "Tag Probe" <<< "$BODY"; then
  echo "FAIL: mixed #tag + text query failed"
  exit 1
fi
BODY=$(curl -s --get "$BASE/" --data-urlencode "q=#Alpha #Delta")
if ! grep -q "No series match" <<< "$BODY"; then
  echo "FAIL: q=#Alpha #Delta should match nothing"
  exit 1
fi
echo "ok: #tag search tokens (AND, mixed with text)"
# 34f. multi-term free text: all terms must match
BODY=$(curl -s --get "$BASE/" --data-urlencode "q=iron wren")
if ! grep -q "Iron Tide" <<< "$BODY"; then
  echo "FAIL: multi-term text search (iron AND wren) missing Iron Tide"
  exit 1
fi
echo "ok: multi-term text search"
# 34g. app.js wires the card mini-tag toggles + sort-default star
JSBODY=$(curl -s "$BASE/static/js/app.js")
for NEEDLE in 'tag-toggle' 'sort-default' 'URLSearchParams'; do
  if ! grep -qF -- "$NEEDLE" <<< "$JSBODY"; then
    echo "FAIL: app.js missing '$NEEDLE'"
    exit 1
  fi
done
echo "ok: app.js tag-toggle + sort-default wiring present"

echo ""
echo "── 35. App icon (favicon + in-app logo) ──"
assert_code GET /static/img/icon.png 200
assert_code GET /static/img/apple-touch-icon.png 200
LIBHTML=$(curl -s "$BASE/")
for NEEDLE in 'rel="icon" type="image/png" href="/static/img/icon.png' \
              'rel="apple-touch-icon"' \
              'class="brand-logo"' 'app.css?v=8' 'app.js?v=7'; do
  if ! grep -qF -- "$NEEDLE" <<< "$LIBHTML"; then
    echo "FAIL: page missing icon/version needle '$NEEDLE'"
    exit 1
  fi
done
if grep -q "🔖" <<< "$LIBHTML"; then
  echo "FAIL: old emoji brand mark still present"
  exit 1
fi
CSSBODY=$(curl -s "$BASE/static/css/app.css")
if ! grep -qF '.brand-logo' <<< "$CSSBODY"; then
  echo "FAIL: app.css missing .brand-logo rule"
  exit 1
fi
echo "ok: favicon + topbar logo served, assets at v7, emoji mark gone"

echo ""
echo "── 36. PWA manifest (installable app) ──"
assert_code GET /static/manifest.json 200
MANIFEST=$(curl -s "$BASE/static/manifest.json")
for NEEDLE in '"short_name": "Fic Tally"' '"display": "standalone"' '"start_url": "/"' '"theme_color": "#15181d"' '"purpose": "maskable"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$MANIFEST"; then
    echo "FAIL: manifest.json missing '$NEEDLE'"
    exit 1
  fi
done
MCTYPE=$(curl -s -o /dev/null -w '%{content_type}' "$BASE/static/manifest.json")
if [[ "$MCTYPE" != application/json* ]]; then
  echo "FAIL: manifest served as $MCTYPE (want application/json)"
  exit 1
fi
for NEEDLE in 'rel="manifest" href="/static/manifest.json' 'name="theme-color" content="#15181d"' 'name="apple-mobile-web-app-capable"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$LIBHTML"; then
    echo "FAIL: layout missing manifest needle '$NEEDLE'"
    exit 1
  fi
done
echo "ok: manifest + install metas served and linked"

echo ""
echo "── 37. Saved views (shelves) ──"
# 37a. save a shelf capturing the current reading filter (the real form
# always posts the EFFECTIVE sort — mirrors that here: sort=updated)
assert_code POST /shelves/save 303 'name=Reading+now&status=reading&q=&type=&tag=&sort=updated'
LIBHTML=$(curl -s "$BASE/")
for NEEDLE in 'class="shelf-chip' '>Reading now</a>' 'href="/?sort=updated&amp;status=reading"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$LIBHTML"; then
    echo "FAIL: library missing shelf chip needle '$NEEDLE'"
    exit 1
  fi
done
echo "ok: shelf saved and rendered as a chip"
# 37b. active shelf highlight: ?status=reading reproduces the shelf exactly
BODY=$(curl -s "$BASE/?status=reading")
if ! grep -qF 'class="shelf-chip active"' <<< "$BODY"; then
  echo "FAIL: shelf not highlighted on its own view"
  exit 1
fi
# ...but a different view must NOT highlight it
BODY=$(curl -s "$BASE/?status=dropped")
if grep -qF 'class="shelf-chip active"' <<< "$BODY"; then
  echo "FAIL: shelf highlighted on a non-matching view"
  exit 1
fi
echo "ok: shelf active-state matches exactly"
# 37d. re-saving the same name replaces (params update, no dup chip)
assert_code POST /shelves/save 303 'name=Reading+now&status=reading&q=&type=&tag=&sort=title'
BODY=$(curl -s "$BASE/")
N=$(grep -oF 'Reading now' <<< "$BODY" | wc -l)
if [ "$N" -lt 2 ]; then  # chip + delete button + possibly summary
  echo "FAIL: shelf chip or delete entry missing (found $N occurrences)"
  exit 1
fi
if ! grep -qF 'href="/?sort=title&amp;status=reading"' <<< "$BODY"; then
  echo "FAIL: replaced shelf should point at ?sort=title&status=reading"
  exit 1
fi
echo "ok: re-save replaces the shelf in place"
# 37e. validation: empty name, nameless view, bogus enum → 400
assert_code POST /shelves/save 400 'name=&status=reading&q=&type=&tag=&sort='
assert_code POST /shelves/save 400 'name=Everything&q=&type=&tag=&sort='
assert_code POST /shelves/save 400 'name=Bad&status=nope&q=&type=&tag=&sort='
# bogus status must not have corrupted the stored list
BODY=$(curl -s "$BASE/")
if ! grep -qF 'Reading now' <<< "$BODY"; then
  echo "FAIL: shelf list corrupted by rejected write"
  exit 1
fi
echo "ok: shelf validation + no corruption after 400s"
# 37f. shelves visible in /api/settings (follow the database)
SETTINGS=$(curl -s "$BASE/api/settings")
if ! grep -qF '"shelves"' <<< "$SETTINGS"; then
  echo "FAIL: GET /api/settings missing shelves group"
  exit 1
fi
echo "ok: shelves exposed via settings API"
# 37g. delete
assert_code POST /shelves/delete 303 'name=Reading+now&back='
BODY=$(curl -s "$BASE/")
if grep -qF 'shelf-chip">Reading now' <<< "$BODY" && grep -qF '× Reading now' <<< "$BODY"; then
  echo "FAIL: shelf still present after delete"
  exit 1
fi
echo "ok: shelf deleted"

echo ""
echo "── 38. Per-series reading history + pace ──"
# 38a. clean slate: moonlit has had no progress updates in any earlier
# group, so its detail page shows the empty-history state
BODY=$(curl -s "$BASE/series/moonlit-cartographer")
if ! grep -qF 'Reading history' <<< "$BODY" || ! grep -qF 'No updates logged yet' <<< "$BODY"; then
  echo "FAIL: empty history state missing"
  exit 1
fi
echo "ok: empty history state rendered"
# 38b. +1 twice and Set on iron-tide → new log rows, newest first
# (iron-tide sits at ch. 143 from the earlier streak group)
assert_code POST /series/iron-tide/progress 303 'btn_plus=1&chapter_label='
assert_code POST /series/iron-tide/progress 303 'btn_plus=1&chapter_label='
assert_code POST /series/iron-tide/progress 303 'btn_set=1&chapter_set=145.5&chapter_label='
BODY=$(curl -s "$BASE/series/iron-tide")
for NEEDLE in 'history-row' '145.5' 'history-delta'; do
  if ! grep -qF -- "$NEEDLE" <<< "$BODY"; then
    echo "FAIL: history list missing '$NEEDLE'"
    exit 1
  fi
done
N=$(grep -oF 'history-row' <<< "$BODY" | wc -l)
if [ "$N" -lt 3 ]; then
  echo "FAIL: expected >=3 history rows, got $N"
  exit 1
fi
# newest first: flatten, slice from history-list, first chapter value = 145.5
FLAT=$(curl -s "$BASE/series/iron-tide" | tr -d '\n')
HIST=${FLAT#*history-list}
FIRSTCH=$(grep -oE 'history-ch">[^<]+' <<< "$HIST" | head -1 | sed 's/.*>//')
if [ "$FIRSTCH" != "145.5" ]; then
  echo "FAIL: history not newest-first (first chapter value: '$FIRSTCH')"
  exit 1
fi
echo "ok: +1 / Set logged, newest first"
# 38c. chapters this week appears in the summary
if ! grep -qF 'this week' <<< "$BODY"; then
  echo "FAIL: 'this week' counter missing from history summary"
  exit 1
fi
echo "ok: chapters-this-week counter rendered"
# 38d. no-op set does NOT log a new row (set same 145.5 again)
assert_code POST /series/iron-tide/progress 303 'btn_set=1&chapter_set=145.5&chapter_label='
BODY2=$(curl -s "$BASE/series/iron-tide")
N2=$(grep -oF 'history-row' <<< "$BODY2" | wc -l)
if [ "$N2" != "$N" ]; then
  echo "FAIL: no-op set added a log row ($N → $N2)"
  exit 1
fi
echo "ok: no-op update not logged"
# 38e. clear num logs a row with NULL chapter display
assert_code POST /series/iron-tide/progress 303 'btn_clear_num=1&chapter_label='
BODY=$(curl -s "$BASE/series/iron-tide")
if ! grep -qF '—' <<< "$BODY"; then
  echo "FAIL: cleared-chapter row missing em-dash display"
  exit 1
fi
# restore the chapter for later groups
assert_code POST /series/iron-tide/progress 303 'btn_set=1&chapter_set=142&chapter_label='
echo "ok: clear-num logged with fallback display"
# 38f. pace line stays hidden: all log entries are from one day, and the
# estimate requires 2+ distinct days of positive progress.
BODY=$(curl -s "$BASE/series/iron-tide")
if grep -qF 'pace-line' <<< "$BODY"; then
  echo "FAIL: pace estimate shown with single-day history (should be hidden)"
  exit 1
fi
echo "ok: pace estimate hidden without 2+ distinct days"

echo ""
echo "── 39. Bulk status changes ──"
# 39a. bulk form scaffolding present on the library page
LIBHTML=$(curl -s "$BASE/")
for NEEDLE in 'bulk-form' 'name="series_ids"' 'action="/bulk/status"' 'With selected:'; do
  if ! grep -qF -- "$NEEDLE" <<< "$LIBHTML"; then
    echo "FAIL: library missing bulk needle '$NEEDLE'"
    exit 1
fi
done
# checkboxes must be SIBLINGS of the card link, not children (click-safe)
if grep -qF '<label class="card-check" title="Select ' <<< "$LIBHTML"; then
  echo "ok: per-card checkboxes rendered"
else
  echo "FAIL: card checkboxes missing"
  exit 1
fi
echo "ok: bulk form + per-card checkboxes present"
# 39b. apply: move both seeds to 'on hold' in one POST
assert_code POST /bulk/status 303 'series_ids=iron-tide&series_ids=moonlit-cartographer&status=on+hold&back='
BODY=$(curl -s "$BASE/?status=on+hold")
if ! grep -q "Iron Tide" <<< "$BODY" || ! grep -q "Moonlit Cartographer" <<< "$BODY"; then
  echo "FAIL: bulk status not applied to both series"
  exit 1
fi
BODY=$(curl -s "$BASE/?status=reading")
if grep -q "Iron Tide" <<< "$BODY"; then
  echo "FAIL: Iron Tide still reading after bulk change"
  exit 1
fi
echo "ok: bulk status applied to both series"
# 39c. PRG back-link: the back query is honored (urlencoded: ?sort=title)
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' -X POST "$BASE/bulk/status" --data 'series_ids=iron-tide&status=reading&back=%3Fsort%3Dtitle')
if [[ "$LOC" != *"sort=title"* ]]; then
  echo "FAIL: bulk redirect ignored back query ($LOC)"
  exit 1
fi
assert_code POST /bulk/status 303 'series_ids=iron-tide&status=reading&back=%3Fsort%3Dtitle'
echo "ok: bulk redirect honors the back query"
# 39d. validation: missing/invalid status → 400
assert_code POST /bulk/status 400 'series_ids=iron-tide&status=&back='
assert_code POST /bulk/status 400 'series_ids=iron-tide&status=what&back='
echo "ok: bulk status validated"
# 39e. empty selection is a harmless no-op redirect
assert_code POST /bulk/status 303 'series_ids=&status=reading&back='
echo "ok: empty selection no-ops"
# restore moonlit to reading for later groups
assert_code POST /bulk/status 303 'series_ids=moonlit-cartographer&status=reading&back='

echo ""
echo "── 40. One-click full backup ──"
# 40a. /backup serves a zip attachment
BK=/tmp/fic-tally-backup-test.zip
CODE=$(curl -s -o "$BK" -w '%{http_code}' "$BASE/backup")
if [ "$CODE" != "200" ]; then
  echo "FAIL: GET /backup → $CODE"
  exit 1
fi
BCTYPE=$(curl -s -o /dev/null -w '%{content_type}' "$BASE/backup")
if [[ "$BCTYPE" != application/zip* ]]; then
  echo "FAIL: backup content-type is $BCTYPE"
  exit 1
fi
BCCD=$(curl -s -D - -o /dev/null "$BASE/backup" | tr -d '\r' | grep -i '^content-disposition:' | head -1)
if [[ "$BCCD" != *'attachment; filename="fic-tally-backup-'* ]]; then
  echo "FAIL: Content-Disposition attachment filename missing ($BCCD)"
  exit 1
fi
echo "ok: /backup serves a zip attachment"
# 40b. archive contents: db snapshot + restore notes (covers dir may be empty).
# Capture unzip -l into a variable first — piping a slow producer into
# `grep -q` under pipefail dies with SIGPIPE (141) the moment grep exits
# early (see the note at the top of this file).
BKLIST=$(unzip -l "$BK")
if ! grep -q 'fic-tally.db' <<< "$BKLIST"; then
  echo "FAIL: backup zip missing fic-tally.db"
  exit 1
fi
if ! grep -q 'RESTORE.txt' <<< "$BKLIST"; then
  echo "FAIL: backup zip missing RESTORE.txt"
  exit 1
fi
echo "ok: zip contains db snapshot + RESTORE.txt"
# 40c. the snapshot is a REAL sqlite db carrying everything: series rows,
# settings (incl. shelves from group 37's rejected-write aftermath), and the
# chapter_log history from group 38. (python3 sqlite3 module — no CLI dep.)
rm -rf /tmp/fic-tally-bk && mkdir -p /tmp/fic-tally-bk
unzip -q -o "$BK" -d /tmp/fic-tally-bk
LOGROWS=$(python3 -c "import sqlite3;print(sqlite3.connect('/tmp/fic-tally-bk/fic-tally.db').execute('SELECT COUNT(*) FROM chapter_log').fetchone()[0])" 2>/dev/null || echo ERR)
if [ "$LOGROWS" = "ERR" ] || [ "$LOGROWS" -lt 4 ]; then
  echo "FAIL: snapshot chapter_log rows = '$LOGROWS' (want >=4)"
  exit 1
fi
SERIESN=$(python3 -c "import sqlite3;print(sqlite3.connect('/tmp/fic-tally-bk/fic-tally.db').execute('SELECT COUNT(*) FROM series').fetchone()[0])" 2>/dev/null || echo ERR)
if [ "$SERIESN" = "ERR" ] || [ "$SERIESN" -lt 2 ]; then
  echo "FAIL: snapshot series rows = '$SERIESN'"
  exit 1
fi
echo "ok: snapshot db carries series + reading history"
# 40d. uploaded covers are included (upload one first: a tiny valid 1x1 PNG)
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82' > /tmp/fic-tally-tiny.png
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/iron-tide/cover" -F "cover=@/tmp/fic-tally-tiny.png")
if [ "$CODE" != "303" ]; then
  echo "FAIL: cover upload before backup re-test → $CODE"
  exit 1
fi
curl -s -o "$BK" "$BASE/backup"
BKLIST=$(unzip -l "$BK")
if ! grep -q 'covers/iron-tide.png' <<< "$BKLIST"; then
  echo "FAIL: backup zip missing uploaded cover"
  exit 1
fi
echo "ok: uploaded covers included in the archive"
# clean up the probe cover
assert_code POST /series/iron-tide/cover/delete 303

echo ""
echo "── 41. Progress % + tag autocomplete + cover drop + timestamps ──"
# 41a. % read on cards and on the detail progress meta ("+" escapes to &#43;)
BODY=$(curl -s "$BASE/")
if ! grep -qF '142 / 210&#43; · 67%' <<< "$BODY"; then
  echo "FAIL: card progress percentage missing (142/210+ should show 67%)"
  exit 1
fi
BODY=$(curl -s "$BASE/series/iron-tide")
if ! grep -qF '67%' <<< "$BODY"; then
  echo "FAIL: detail progress-meta percentage missing"
  exit 1
fi
echo "ok: progress percentage on cards + detail"
# 41b. absolute timestamps on hover for card + detail relative times
BODY=$(curl -s "$BASE/")
if ! grep -qF 'title="Last read ' <<< "$BODY"; then
  echo "FAIL: card-updated tooltip missing"
  exit 1
fi
BODY=$(curl -s "$BASE/series/iron-tide")
if ! grep -qF 'title="Last read ' <<< "$BODY"; then
  echo "FAIL: detail last-read tooltip missing"
  exit 1
fi
echo "ok: exact timestamps on hover"
# 41c. tag autocomplete: data-tags carries existing tags on the edit form.
# (html/template escapes the JSON quotes inside the attribute as &#34; —
# the browser decodes entities before JS reads dataset.tags.)
BODY=$(curl -s "$BASE/series/iron-tide/edit")
if ! grep -qF 'data-tags=' <<< "$BODY"; then
  echo "FAIL: edit form missing data-tags attribute"
  exit 1
fi
if ! grep -qF '&#34;Isekai&#34;' <<< "$BODY"; then
  echo "FAIL: data-tags does not carry the seeded Isekai tag"
  exit 1
fi
if ! grep -qF 'id="tag-ac"' <<< "$BODY"; then
  echo "FAIL: tag suggestion popover element missing"
  exit 1
fi
BODY=$(curl -s "$BASE/series/new")
if ! grep -qF 'data-tags=' <<< "$BODY"; then
  echo "FAIL: add form missing data-tags (autocomplete)"
  exit 1
fi
echo "ok: tag autocomplete wired on add + edit forms"
# 41d. cover drop zone markers on the edit form
BODY=$(curl -s "$BASE/series/iron-tide/edit")
for NEEDLE in 'data-cover-drop="1"' 'class="cover-drop-badge"' 'for="cover"' 'Drop an image on the cover'; do
  if ! grep -qF -- "$NEEDLE" <<< "$BODY"; then
    echo "FAIL: edit form missing cover-drop needle '$NEEDLE'"
    exit 1
  fi
done
echo "ok: cover drop zone + preview scaffolding present"
# 41e. JS + CSS wiring for all three enhancements
JSBODY=$(curl -s "$BASE/static/js/app.js")
for NEEDLE in 'tag-ac-item' 'URL.createObjectURL' 'series_ids' 'cover-drop'; do
  if ! grep -qF -- "$NEEDLE" <<< "$JSBODY"; then
    echo "FAIL: app.js missing enhancement needle '$NEEDLE'"
    exit 1
  fi
done
CSSBODY=$(curl -s "$BASE/static/css/app.css")
for NEEDLE in '.tag-ac' '.cover-drop.dragover' '.card-check' '.shelf-chip' '.bulk-bar' '.history-row' '.pace-line'; do
  if ! grep -qF -- "$NEEDLE" <<< "$CSSBODY"; then
    echo "FAIL: app.css missing style needle '$NEEDLE'"
    exit 1
  fi
done
echo "ok: JS + CSS wiring for all enhancements"

echo ""
echo "── 42. Options page: new publication-status vocabulary ──"
assert_code GET /options 200
OPTBODY=$(curl -s "$BASE/options")
# new default labels render (Ongoing / Complete / Hiatus / Canceled)
# — labels are <input value="…"> attributes on this page, not text nodes
for NEEDLE in 'Dropdown options' 'value="Complete"' 'value="Hiatus"' 'value="Canceled"' 'value="Reading"' 'value="Manga"' 'built-in'; do
  if ! grep -qF -- "$NEEDLE" <<< "$OPTBODY"; then
    echo "FAIL: options page missing needle '$NEEDLE'"
    exit 1
  fi
done
# "Upcoming" is gone from the vocabulary entirely
if grep -q 'Upcoming' <<< "$OPTBODY"; then
  echo "FAIL: options page still mentions Upcoming"
  exit 1
fi
# values (import IDs) stay canonical — the value/label split is the point
for NEEDLE in '>ongoing<' '>completed<' '>hiatus<' '>cancelled<' '>reading<' '>plan to read<'; do
  if ! grep -qF -- "$NEEDLE" <<< "$OPTBODY"; then
    echo "FAIL: options page missing value needle '$NEEDLE'"
    exit 1
  fi
done
# nav link present on every page
if ! grep -qF 'href="/options"' <<< "$(curl -s "$BASE/")"; then
  echo "FAIL: library page missing Options nav link"
  exit 1
fi
# add form pairs the old value with the new label
if ! grep -qF 'value="completed">Complete' <<< "$(curl -s "$BASE/series/new")"; then
  echo "FAIL: add form should render value=completed with label Complete"
  exit 1
fi
echo "ok: /options renders new labels with stable values; Upcoming removed"

echo ""
echo "── 43. Options management: rename / add / reorder / remove ──"
# 43a. rename a label (dropped → Paused); value must stay "dropped"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/options/save" --data "label_status_dropped=Paused")
[ "$GOT" = "303" ] || { echo "FAIL: rename option → $GOT"; exit 1; }
if ! grep -qF 'value="dropped">Paused' <<< "$(curl -s "$BASE/series/new")"; then
  echo "FAIL: renamed label should pair with the unchanged value"
  exit 1
fi
assert_body /stats "Paused"
echo "ok: label rename flows to forms + stats, value untouched"
# 43b. add a custom type; create a series that uses it (title chosen to
# dodge the fuzzy dup-checker — the suite leaves a "Tag Probe" fixture
# behind and "… Probe" would trip the 50% token-overlap heuristic)
curl -s -o /dev/null -X POST "$BASE/options/save" --data "add_type=Webtoon"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/series/new" --data "title=Webtoon+Trial&type=webtoon&cover_url=&parent_id=" -H 'Content-Type: application/x-www-form-urlencoded')
[ "$GOT" = "303" ] || { echo "FAIL: create series with custom type → $GOT"; exit 1; }
assert_body "/series/webtoon-trial" "Webtoon"
if ! grep -qF 'value="webtoon">Webtoon' <<< "$(curl -s "$BASE/series/new")"; then
  echo "FAIL: custom type missing from type select"
  exit 1
fi
echo "ok: custom option added, selectable, renders on detail"
# 43c. reorder via position (manga last) — select order follows. Byte offsets
# of the first occurrence decide the order (grep -bo), since plain alternation
# would capture both options regardless of which sorts first.
curl -s -o /dev/null -X POST "$BASE/options/save" --data "pos_type_manga=9"
ADDHTML=$(curl -s "$BASE/series/new")
MANGA_AT=$(grep -bo 'value="manga"' <<< "$ADDHTML" | head -1 | cut -d: -f1)
MANHWA_AT=$(grep -bo 'value="manhwa"' <<< "$ADDHTML" | head -1 | cut -d: -f1)
if [ -z "$MANGA_AT" ] || [ -z "$MANHWA_AT" ] || [ "$MANGA_AT" -le "$MANHWA_AT" ]; then
  echo "FAIL: manga should sort after manhwa now (manga@$MANGA_AT manhwa@$MANHWA_AT)"
  exit 1
fi
curl -s -o /dev/null -X POST "$BASE/options/save" --data "pos_type_manga=1"
echo "ok: position input reorders the dropdown"
# 43d. removal guards: in-use → 400, protected → 400
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/options/save" --data "del_type_webtoon=1")
[ "$GOT" = "400" ] || { echo "FAIL: removing an in-use option should 400, got $GOT"; exit 1; }
for DEL in "del_status_completed=1" "del_pub_status_completed=1"; do
  GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/options/save" --data "$DEL")
  [ "$GOT" = "400" ] || { echo "FAIL: removing a built-in option ($DEL) should 400, got $GOT"; exit 1; }
done
echo "ok: in-use + built-in options refuse removal"
# 43e. removing an unused custom option works (add pub oneshot → remove it)
curl -s -o /dev/null -X POST "$BASE/options/save" --data "add_pub_status=Oneshot"
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/options/save" --data "del_pub_status_oneshot=1")
[ "$GOT" = "303" ] || { echo "FAIL: removing unused custom option → $GOT"; exit 1; }
if grep -q 'value="oneshot"' <<< "$(curl -s "$BASE/series/new")"; then
  echo "FAIL: removed option still offered"
  exit 1
fi
echo "ok: unused custom option removable"
# 43f. validation: duplicate add, empty label, duplicate label (note: the
# "on hold" field name contains a space — encode it)
for BAD in "add_type=Manga" "label_status_on%20hold=" "label_status_on%20hold=Reading"; do
  GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/options/save" --data "$BAD")
  [ "$GOT" = "400" ] || { echo "FAIL: invalid options payload '$BAD' should 400, got $GOT"; exit 1; }
done
# options cannot be smuggled through the settings API
GOT=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/settings" -H 'Content-Type: application/json' --data '{"options":{"status":[]}}')
[ "$GOT" = "400" ] || { echo "FAIL: settings API should reject the options group, got $GOT"; exit 1; }
# 43g. import validates against the live lists: a bogus pub_status now lists
# the CURRENT values, and the legacy 'upcoming' value is rejected
RESP=$(curl -s -X POST "$BASE/api/series/batch" -H 'Content-Type: application/json' --data '{"series":[{"title":"OptBad","type":"manga","pub_status":"upcoming"}]}')
if ! grep -q "unknown publication status 'upcoming'" <<< "$RESP"; then
  echo "FAIL: import should reject the retired 'upcoming' value: $RESP"
  exit 1
fi
echo "ok: validation rejects dup/empty labels + retired values; options group locked out of settings API"
# 43h. cleanup: delete the trial series, drop the custom type, restore the label
curl -s -o /dev/null -X POST "$BASE/series/webtoon-trial/delete"
curl -s -o /dev/null -X POST "$BASE/options/save" --data "del_type_webtoon=1"
curl -s -o /dev/null -X POST "$BASE/options/save" --data "label_status_dropped=Dropped"
if ! grep -qF 'value="dropped">Dropped' <<< "$(curl -s "$BASE/series/new")"; then
  echo "FAIL: label restore failed"
  exit 1
fi
echo "ok: group fixtures cleaned up"

echo ""
echo "── 44. Bulk mode: hidden by default + Select-multiple toggle ──"
LIBHTML=$(curl -s "$BASE/")
for NEEDLE in 'id="bulk-mode"' 'class="bulk-mode-btn"' 'for="bulk-mode"' 'Select multiple' 'name="series_ids"'; do
  if ! grep -qF -- "$NEEDLE" <<< "$LIBHTML"; then
    echo "FAIL: library page missing bulk-mode needle '$NEEDLE'"
    exit 1
  fi
done
# the toggle checkbox must be nameless — it must never be submitted with the form
if grep -q 'id="bulk-mode" name=' <<< "$LIBHTML"; then
  echo "FAIL: bulk-mode checkbox must not carry a name"
  exit 1
fi
CSSBODY=$(curl -s "$BASE/static/css/app.css")
for NEEDLE in '.bulk-mode-btn' '.bulk-form:has(#bulk-mode:checked) .bulk-bar' '.bulk-form:has(#bulk-mode:checked) .card-check' 'section.view:has(#bulk-mode:checked) .bulk-mode-btn'; do
  if ! grep -qF -- "$NEEDLE" <<< "$CSSBODY"; then
    echo "FAIL: app.css missing bulk-mode rule '$NEEDLE'"
    exit 1
  fi
done
# card checkboxes exist in the DOM (CSS hides them until the toggle is on)
if ! grep -qF 'class="card-check"' <<< "$LIBHTML"; then
  echo "FAIL: card checkboxes missing from the grid form"
  exit 1
fi
echo "ok: bulk checkboxes hidden behind the Select-multiple toggle (pure CSS)"
# 44b. migration: a pre-v8 DB holding pub_status='upcoming' is cleared once,
# the options group is seeded, and the page renders with the new vocabulary.
# The old-style DB is staged from the /backup zip (a consistent VACUUM INTO
# snapshot — copying the live .db directly would miss WAL-resident writes).
MIGZIP=/tmp/ft-mig.zip
MIGDB=/tmp/ft-mig.db
rm -f "$MIGZIP" "$MIGDB" "$MIGDB-wal" "$MIGDB-shm"
curl -s "$BASE/backup" -o "$MIGZIP"
python3 - "$MIGZIP" "$MIGDB" <<'PYEOF'
import sqlite3, sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as z:
    with z.open("fic-tally.db") as src, open(sys.argv[2], "wb") as dst:
        dst.write(src.read())
db = sqlite3.connect(sys.argv[2])
db.execute("UPDATE series SET pub_status='upcoming' WHERE id='iron-tide'")
db.execute("DELETE FROM settings WHERE key='options'")
db.commit()
print("staged:", db.execute("SELECT id, pub_status FROM series WHERE id='iron-tide'").fetchone())
PYEOF
MIGADDR="127.0.0.1:4399"
./fic-tally -addr "$MIGADDR" -db "$MIGDB" > /tmp/ft-mig.log 2>&1 &
MIGPID=$!
sleep 1.2
# the add form pairs the untouched value with the new label — proves the
# migrated server serves the new vocabulary end-to-end
MIGHTML=$(curl -s "http://$MIGADDR/series/new")
if ! grep -qF 'value="completed">Complete' <<< "$MIGHTML"; then
  echo "FAIL: migrated server should render the new Complete label"
  kill $MIGPID 2>/dev/null || true
  exit 1
fi
kill $MIGPID 2>/dev/null || true
wait $MIGPID 2>/dev/null || true
python3 - "$MIGDB" <<'PYEOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
pub = db.execute("SELECT pub_status FROM series WHERE id='iron-tide'").fetchone()[0]
opt = db.execute("SELECT value FROM settings WHERE key='options'").fetchone()[0]
assert pub == "", f"upcoming should be cleared to '', got {pub!r}"
assert '"label":"Complete"' in opt, "options group should be seeded with new labels"
assert 'upcoming' not in opt, "upcoming must not be in the seeded vocabulary"
print("migration verified: pub_status cleared + options seeded")
PYEOF
rm -f "$MIGDB" "$MIGDB-wal" "$MIGDB-shm"
echo "ok: one-time migration clears 'upcoming' and seeds the options group"

echo ""
echo "════════════════════════════════════"
echo "ALL SMOKE TESTS PASSED"
echo "════════════════════════════════════"
