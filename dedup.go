package main

// dedup.go — duplicate-title detection.
//
// Goal: warn before a second entry for the same work lands in the library,
// without a metadata service or an alias database. Matching considers BOTH
// the main title and any alternative titles on either side, so
// "Akatsuki no Yona" matches a stored alt title "Yona of the Dawn" exactly.
// Three signals, cheapest first:
//
//  1. normalizedExact — lowercase, strip punctuation/diacritics-folding
//     placeholder, collapse whitespace. Catches "Iron Tide!" vs "iron tide"
//     and any title-vs-alt-title pair.
//  2. levenshtein ratio ≥ 0.8 — catches typos and small re-orderings
//     ("Iorn Tide", "Iron Tide Part 2" near-misses).
//  3. significant-token overlap ≥ 0.5 (of the smaller token set) — catches
//     translated aliases like "Akatsuki no Yona" vs "Yona of the Dawn",
//     where edit distance is useless but the distinctive word matches.
//
// Signals 2 and 3 are advisories: the add form shows a warning and lets the
// user confirm; import/API policies (skip/update) act on exact matches and
// merely annotate fuzzy ones, so a false positive never silently drops data.

import (
        "strconv"
        "strings"
)

// dupStopwords are tokens ignored by the overlap test: they're common in
// titles on both sides of a translation, so matching on them is noise.
var dupStopwords = map[string]bool{
        "the": true, "a": true, "an": true, "of": true, "and": true,
        "no": true, "na": true, "to": true, "in": true, "wa": true,
        "ga": true, "wo": true, "for": true, "on": true,
}

const (
        dupLevenshteinThreshold = 0.80 // ratio of similarity, 0..1
        dupTokenOverlapThreshold = 0.50 // fraction of the smaller significant-token set
)

// DupCandidate is one existing series flagged as a possible duplicate.
type DupCandidate struct {
        ID     string
        Title  string
        Reason string // human-readable, e.g. "exact title match" or "similar title (86%)"
        Strong bool   // true = normalized-exact match (import skip/update acts only on these)
}

// normalizeTitle lowercases, strips everything that isn't a letter/digit,
// and collapses runs of separators to single spaces. Unicode letters pass
// through unchanged (case-folded).
func normalizeTitle(s string) string {
        s = strings.ToLower(strings.TrimSpace(s))
        var b strings.Builder
        prevSpace := false
        for _, r := range s {
                switch {
                case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 0x80: // keep non-ASCII letters (CJK etc.)
                        b.WriteRune(r)
                        prevSpace = false
                default:
                        if !prevSpace && b.Len() > 0 {
                                b.WriteRune(' ')
                                prevSpace = true
                        }
                }
        }
        return strings.TrimSpace(b.String())
}

// significantTokens splits a normalized title into tokens minus stopwords.
// Tokens shorter than 3 runes are also dropped ("no", "wo", "1" ...).
func significantTokens(normalized string) []string {
        fields := strings.Fields(normalized)
        out := make([]string, 0, len(fields))
        for _, f := range fields {
                if dupStopwords[f] {
                        continue
                }
                if len([]rune(f)) < 3 {
                        continue
                }
                out = append(out, f)
        }
        return out
}

// levenshtein computes the classic edit distance. Titles are short (< 200
// runes); the O(n·m) loop with a rolling row is plenty.
func levenshtein(a, b string) int {
        ra, rb := []rune(a), []rune(b)
        if len(ra) == 0 {
                return len(rb)
        }
        if len(rb) == 0 {
                return len(ra)
        }
        prev := make([]int, len(rb)+1)
        curr := make([]int, len(rb)+1)
        for j := 0; j <= len(rb); j++ {
                prev[j] = j
        }
        for i := 1; i <= len(ra); i++ {
                curr[0] = i
                for j := 1; j <= len(rb); j++ {
                        cost := 1
                        if ra[i-1] == rb[j-1] {
                                cost = 0
                        }
                        curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
                }
                prev, curr = curr, prev
        }
        return prev[len(rb)]
}

func min3(a, b, c int) int {
        if b < a {
                a = b
        }
        if c < a {
                a = c
        }
        return a
}

// similarity is 1 - editDistance/maxLen, clamped to [0,1].
func similarity(a, b string) float64 {
        if a == b {
                return 1
        }
        maxLen := len([]rune(a))
        if l := len([]rune(b)); l > maxLen {
                maxLen = l
        }
        if maxLen == 0 {
                return 1
        }
        d := levenshtein(a, b)
        ratio := 1 - float64(d)/float64(maxLen)
        if ratio < 0 {
                return 0
        }
        return ratio
}

// tokenOverlap returns the intersection size over the smaller significant
// token-set size, plus the shared tokens (for the warning message).
func tokenOverlap(a, b string) (float64, []string) {
        ta, tb := significantTokens(a), significantTokens(b)
        if len(ta) == 0 || len(tb) == 0 {
                return 0, nil
        }
        set := make(map[string]bool, len(ta))
        for _, t := range ta {
                set[t] = true
        }
        var shared []string
        seen := map[string]bool{}
        for _, t := range tb {
                if set[t] && !seen[t] {
                        shared = append(shared, t)
                        seen[t] = true
                }
        }
        smaller := len(ta)
        if len(tb) < smaller {
                smaller = len(tb)
        }
        if smaller == 0 {
                return 0, shared
        }
        return float64(len(shared)) / float64(smaller), shared
}

// normalizeAll normalizes every name, drops empties, and dedupes. Used to
// build the "matchable names" set of a series: main title + alt titles.
func normalizeAll(names ...string) []string {
        out := make([]string, 0, len(names))
        seen := map[string]bool{}
        for _, n := range names {
                v := normalizeTitle(n)
                if v != "" && !seen[v] {
                        seen[v] = true
                        out = append(out, v)
                }
        }
        return out
}

// findDuplicates returns existing series that may be the same work as the
// given title (+ alternative titles). Both sides' main AND alternative
// titles participate in matching, so an exact alt-title hit ranks as a
// strong duplicate. Ordered strongest match first. excludeID lets callers
// skip the series being edited (a series is not a duplicate of itself).
func findDuplicates(existing []EntryWithSeries, title string, altTitles []string, excludeID string) []DupCandidate {
        inNorms := normalizeAll(append([]string{title}, altTitles...)...)
        if len(inNorms) == 0 {
                return nil
        }
        inTitle := normalizeTitle(title)
        var out []DupCandidate
        for _, e := range existing {
                if e.ID == excludeID {
                        continue
                }
                eNorms := normalizeAll(append([]string{e.Title}, e.AltTitles...)...)
                if len(eNorms) == 0 {
                        continue
                }
                eTitle := normalizeTitle(e.Title)

                // Signal 1: any incoming name equals any stored name (strong).
                strong, titleTitle, firstPair := false, false, ""
                for _, in := range inNorms {
                        for _, en := range eNorms {
                                if in == en {
                                        strong = true
                                        if in == inTitle && en == eTitle {
                                                titleTitle = true
                                        }
                                        if firstPair == "" {
                                                firstPair = in
                                        }
                                }
                        }
                }
                if strong {
                        reason := "exact title match"
                        if !titleTitle {
                                reason = "matches alternative title \"" + firstPair + "\""
                        }
                        out = append(out, DupCandidate{
                                ID:     e.ID,
                                Title:  e.Title,
                                Reason: reason,
                                Strong: true,
                        })
                        continue
                }

                // Signals 2+3: best fuzzy score across all name pairs.
                bestSim := 0.0
                bestOverlap, bestShared := 0.0, []string(nil)
                for _, in := range inNorms {
                        for _, en := range eNorms {
                                if s := similarity(in, en); s > bestSim {
                                        bestSim = s
                                }
                                if r, sh := tokenOverlap(in, en); r > bestOverlap {
                                        bestOverlap, bestShared = r, sh
                                }
                        }
                }
                if bestSim >= dupLevenshteinThreshold {
                        out = append(out, DupCandidate{
                                ID:     e.ID,
                                Title:  e.Title,
                                Reason: "very similar title (" + pct(bestSim) + " match)",
                        })
                        continue
                }
                if bestOverlap >= dupTokenOverlapThreshold && len(bestShared) > 0 {
                        out = append(out, DupCandidate{
                                ID:     e.ID,
                                Title:  e.Title,
                                Reason: "shares key word(s): " + strings.Join(bestShared, ", "),
                        })
                }
        }
        return out
}

// pct formats a 0..1 ratio as an integer percentage.
func pct(ratio float64) string {
        return strconv.Itoa(int(ratio*100)) + "%"
}
