package main

// stats.go — the reading statistics dashboard (GET /stats).
//
// Everything is computed in Go from Store.List() + Store.ReadDays(); there
// are no aggregate SQL queries to keep in sync with the schema. The library
// is single-user and small, so O(n) passes per page view are nothing.
//
// Streak rules:
//   - a "reading day" is a UTC date with ≥1 chapter advance (daily_reads)
//   - the current streak counts consecutive reading days ending today, or
//     ending yesterday when today has no reads yet (the streak isn't broken
//     until a full day passes without reading)
//   - the longest streak walks the whole daily_reads history

import (
        "net/http"
        "sort"
        "strconv"
        "time"
)

// activityDay is one bar in the 30-day activity strip.
type activityDay struct {
        Date  string // "2006-01-02"
        Label string // "Aug 26"
        Count int
        Pct   int // bar height, 0-100 (relative to the window's max)
}

// tagCount pairs a tag with how many series carry it.
type tagCount struct {
        Tag   string
        Count int
}

// statsVM is the view model passed to stats.html.
type statsVM struct {
        // headline cards
        CurrentlyReading int
        CompletedThisMonth int
        AvgRating        string // "7.5" or "—" when nothing is rated
        CurrentStreak    int
        // secondary line
        TotalSeries    int
        PlanToRead     int
        OnHold         int
        Dropped        int
        CompletedTotal int
        ReadThisWeek   int // series whose last_read_at is within 7 days
        ReadThisMonth  int
        ChaptersTracked float64
        // breakdowns
        StatusBreakdown []breakdownRow
        TypeBreakdown  []breakdownRow
        PubBreakdown   []breakdownRow
        TopTags        []tagCount
        // activity
        Activity []activityDay
        // longest streak
        LongestStreak int
        // recently completed
        RecentlyCompleted []EntryWithSeries
}

type breakdownRow struct {
        Label string
        Count int
        Pct   int // 0-100 of the library total
        Class string // status dot class, "" for types
}

// handleStats renders the dashboard.
func (a *app) handleStats(w http.ResponseWriter, r *http.Request) {
        all, err := a.store.List()
        if err != nil {
                a.serverError(w, r, "list for stats", err)
                return
        }
        days, err := a.store.ReadDays()
        if err != nil {
                a.serverError(w, r, "read days for stats", err)
                return
        }

        now := time.Now().UTC()
        vm := computeStats(all, days, now, a.options())

        a.render(w, r, "stats.html", map[string]any{
                "Title": "Reading stats",
                "Stats": vm,
        })
}

// computeStats is pure so it can be unit-tested without a store. opts
// supplies the user-editable option lists: the breakdown rows iterate them
// (in dropdown order, with current labels), so renamed and custom options
// appear automatically. The five fixed tiles (Currently reading / Plan to
// read / On hold / Dropped / Completed) count the built-in VALUES — those
// are protected from removal, so the tiles can never reference a missing
// status.
func computeStats(all []EntryWithSeries, days map[string]int, now time.Time, opts optionLists) statsVM {
        vm := statsVM{TotalSeries: len(all)}

        monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
        weekAgo := now.AddDate(0, 0, -7)
        monthAgo := now.AddDate(0, 0, -30)

        ratingSum, ratingCount := 0, 0
        byStatus := map[string]int{}
        byType := map[string]int{}
        byPub := map[string]int{}
        tagCounts := map[string]int{}

        for _, e := range all {
                byStatus[string(e.Status)]++
                byType[string(e.Type)]++
                byPub[string(e.PubStatus)]++
                for _, t := range e.Tags {
                        tagCounts[t]++
                }
                if e.Rating != nil {
                        ratingSum += *e.Rating
                        ratingCount++
                }
                if e.CurrentChapterNum != nil {
                        vm.ChaptersTracked += *e.CurrentChapterNum
                }
                if !e.LastReadAt.IsZero() {
                        if e.LastReadAt.After(weekAgo) {
                                vm.ReadThisWeek++
                        }
                        if e.LastReadAt.After(monthAgo) {
                                vm.ReadThisMonth++
                        }
                }
        }

        vm.CurrentlyReading = byStatus[string(StatusReading)]
        vm.PlanToRead = byStatus[string(StatusPlanToRead)]
        vm.OnHold = byStatus[string(StatusOnHold)]
        vm.Dropped = byStatus[string(StatusDropped)]
        vm.CompletedTotal = byStatus[string(StatusCompleted)]

        if ratingCount > 0 {
                avg := float64(ratingSum) / float64(ratingCount)
                vm.AvgRating = trimFloatOne(avg)
        } else {
                vm.AvgRating = "—"
        }

        // Completed this month, via completed_at (set on the status transition).
        var completedRecently []EntryWithSeries
        for _, e := range all {
                if e.Status == StatusCompleted && !e.CompletedAt.IsZero() && e.CompletedAt.After(monthStart) {
                        vm.CompletedThisMonth++
                        completedRecently = append(completedRecently, e)
                }
        }
        sort.Slice(completedRecently, func(i, j int) bool {
                return completedRecently[i].CompletedAt.After(completedRecently[j].CompletedAt)
        })
        if len(completedRecently) > 5 {
                completedRecently = completedRecently[:5]
        }
        vm.RecentlyCompleted = completedRecently

        // Status breakdown, in option-list order with current labels.
        for _, op := range opts.Status {
                c := byStatus[op.Value]
                vm.StatusBreakdown = append(vm.StatusBreakdown, breakdownRow{
                        Label: op.Label,
                        Count: c,
                        Pct:   pctOf(c, len(all)),
                        Class: statusDotClass(op.Value),
                })
        }
        // Type breakdown, in option-list order (unused types stay hidden).
        for _, op := range opts.Type {
                c := byType[op.Value]
                if c == 0 {
                        continue
                }
                vm.TypeBreakdown = append(vm.TypeBreakdown, breakdownRow{
                        Label: op.Label,
                        Count: c,
                        Pct:   pctOf(c, len(all)),
                })
        }

        // Publication-status breakdown (ongoing/complete/hiatus/...), in
        // option-list order with current labels. Series with no pub status
        // set fall into an "unknown" row so the counts still add up to the
        // library total.
        unknown := byPub[""]
        for _, op := range opts.PubStatus {
                c := byPub[op.Value]
                if c == 0 {
                        continue
                }
                vm.PubBreakdown = append(vm.PubBreakdown, breakdownRow{
                        Label: op.Label,
                        Count: c,
                        Pct:   pctOf(c, len(all)),
                })
        }
        if unknown > 0 {
                vm.PubBreakdown = append(vm.PubBreakdown, breakdownRow{
                        Label: "Unknown",
                        Count: unknown,
                        Pct:   pctOf(unknown, len(all)),
                })
        }

        // Top tags (top 10, alphabetical within a count).
        vm.TopTags = topTags(tagCounts, 10)

        // Streaks + activity strip.
        vm.CurrentStreak, vm.LongestStreak = streaks(days, now)
        vm.Activity = activityWindow(days, now, 30)

        return vm
}

// streaks returns (current, longest).
func streaks(days map[string]int, now time.Time) (int, int) {
        if len(days) == 0 {
                return 0, 0
        }
        today := now.Format("2006-01-02")
        yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

        // Current streak: consecutive days ending today, or ending yesterday if
        // today isn't logged yet.
        cur := 0
        if days[today] > 0 {
                cur = countBack(days, today)
        } else if days[yesterday] > 0 {
                cur = countBack(days, yesterday)
        }

        // Longest streak: walk every logged date.
        dates := make([]string, 0, len(days))
        for d := range days {
                dates = append(dates, d)
        }
        sort.Strings(dates)
        longest := 0
        run := 0
        prev := ""
        for _, d := range dates {
                if prev != "" {
                        if t1, e1 := time.Parse("2006-01-02", prev); e1 == nil {
                                if t2, e2 := time.Parse("2006-01-02", d); e2 == nil && t2.Sub(t1) == 24*time.Hour {
                                        run++
                                        if run > longest {
                                                longest = run
                                        }
                                        prev = d
                                        continue
                                }
                        }
                }
                run = 1
                if run > longest {
                        longest = run
                }
                prev = d
        }
        return cur, longest
}

// countBack counts consecutive logged days starting at start (inclusive).
func countBack(days map[string]int, start string) int {
        t, err := time.Parse("2006-01-02", start)
        if err != nil {
                return 0
        }
        n := 0
        for {
                key := t.Format("2006-01-02")
                if days[key] == 0 {
                        break
                }
                n++
                t = t.AddDate(0, 0, -1)
        }
        return n
}

// activityWindow builds the last n days (oldest → newest) for the strip.
func activityWindow(days map[string]int, now time.Time, n int) []activityDay {
        out := make([]activityDay, 0, n)
        max := 1
        for i := n - 1; i >= 0; i-- {
                d := now.AddDate(0, 0, -i)
                key := d.Format("2006-01-02")
                c := days[key]
                if c > max {
                        max = c
                }
                out = append(out, activityDay{
                        Date:  key,
                        Label: d.Format("Jan 2"),
                        Count: c,
                })
        }
        for i := range out {
                if out[i].Count > 0 {
                        out[i].Pct = (out[i].Count * 100) / max
                        if out[i].Pct < 8 {
                                out[i].Pct = 8 // keep a visible sliver
                        }
                }
        }
        return out
}

// topTags returns up to n (tag, count) pairs, sorted by count desc then tag asc.
func topTags(m map[string]int, n int) []tagCount {
        out := make([]tagCount, 0, len(m))
        for t, c := range m {
                out = append(out, tagCount{Tag: t, Count: c})
        }
        sort.Slice(out, func(i, j int) bool {
                if out[i].Count != out[j].Count {
                        return out[i].Count > out[j].Count
                }
                return out[i].Tag < out[j].Tag
        })
        if len(out) > n {
                out = out[:n]
        }
        return out
}

// pctOf computes count/total as an integer percentage (0-100).
func pctOf(count, total int) int {
        if total == 0 {
                return 0
        }
        p := (count * 100) / total
        if p > 100 {
                p = 100
        }
        return p
}

// trimFloatOne formats v with exactly one decimal ("7.5", "8.0").
func trimFloatOne(v float64) string {
        return strconv.FormatFloat(v, 'f', 1, 64)
}

// statusDotClass maps a Status to the CSS dot class used in templates.
// Shared by the template FuncMap and the stats breakdown rows.
func statusDotClass(s string) string {
        switch Status(s) {
        case StatusReading:
                return "dot-reading"
        case StatusPlanToRead:
                return "dot-plan"
        case StatusOnHold:
                return "dot-hold"
        case StatusDropped:
                return "dot-dropped"
        case StatusCompleted:
                return "dot-done"
        }
        return "dot-plan"
}
