package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type modeDraw struct {
	Mode  string  `json:"mode"`
	GPU   string  `json:"gpu"`
	Watts float64 `json:"watts"`
	Mins  int     `json:"mins"`
}

type outageRow struct {
	Start  string  `json:"start"`
	End    string  `json:"end"`
	Mins   int     `json:"mins"`
	PctA   int     `json:"pctA"`
	PctB   int     `json:"pctB"`
	BankWh float64 `json:"bankWh"`
}

// drawPoint is one minute of the last run on battery.
type drawPoint struct {
	T     string  `json:"t"` // HH:MM
	Watts float64 `json:"w"`
	Pct   int     `json:"p"`
}

type statsData struct {
	Last      []drawPoint `json:"last"`    // the last run on battery, minute by minute
	LastDay   string      `json:"lastDay"` // its date
	Draws     []modeDraw  `json:"draws"`
	AvgW      float64     `json:"avgW"`
	Outages   []outageRow `json:"outages"`
	WeekCount int         `json:"weekCount"`
	WeekMins  int         `json:"weekMins"`
	Longest   int         `json:"longest"`
	LongestAt string      `json:"longestAt"`
}

func csvRows(text string) [][]string {
	var rows [][]string
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, ";"))
	}
	return rows
}

// BuildStats turns the two journals into what the statistics page shows.
func BuildStats(powerCSV, outagesCSV string, now time.Time) statsData {
	var d statsData
	type acc struct {
		sum float64
		n   int
	}
	groups := map[[2]string]*acc{}
	total := acc{}
	for _, r := range csvRows(powerCSV) {
		if len(r) < 5 {
			continue
		}
		w, err := strconv.ParseFloat(r[2], 64)
		if err != nil || w <= 0 {
			continue
		}
		gpu := r[4]
		if gpu != "awake" {
			gpu = "asleep" // absent, asleep and unknown all mean the card draws nothing
		}
		k := [2]string{r[1], gpu}
		if groups[k] == nil {
			groups[k] = &acc{}
		}
		groups[k].sum += w
		groups[k].n++
		if strings.HasPrefix(r[1], "blackout") {
			total.sum += w
			total.n++
		}
	}
	d.Last, d.LastDay = LastRun(powerCSV, now.Location())
	for k, a := range groups {
		if a.n < 3 {
			continue
		}
		d.Draws = append(d.Draws, modeDraw{Mode: k[0], GPU: k[1], Watts: a.sum / float64(a.n), Mins: a.n})
	}
	sort.Slice(d.Draws, func(i, j int) bool { return d.Draws[i].Watts < d.Draws[j].Watts })
	if total.n > 0 {
		d.AvgW = total.sum / float64(total.n)
	}
	weekAgo := now.AddDate(0, 0, -7)
	for _, r := range csvRows(outagesCSV) {
		if len(r) < 7 {
			continue
		}
		mins, _ := strconv.Atoi(r[2])
		a, _ := strconv.Atoi(r[3])
		b, _ := strconv.Atoi(r[4])
		bank, _ := strconv.ParseFloat(r[6], 64)
		if !OutageWorthLogging(mins, a, b, bank) {
			continue
		}
		d.Outages = append(d.Outages, outageRow{Start: r[0], End: r[1], Mins: mins, PctA: a, PctB: b, BankWh: bank})
		if t, err := time.ParseInLocation("2006-01-02 15:04", r[0], now.Location()); err == nil && t.After(weekAgo) {
			d.WeekCount++
			d.WeekMins += mins
		}
		if mins > d.Longest {
			d.Longest, d.LongestAt = mins, r[0]
		}
	}
	for i, j := 0, len(d.Outages)-1; i < j; i, j = i+1, j-1 {
		d.Outages[i], d.Outages[j] = d.Outages[j], d.Outages[i]
	}
	if len(d.Outages) > 30 {
		d.Outages = d.Outages[:30]
	}
	return d
}

type dayLine struct {
	Date string      `json:"date"`
	Segs [][2]string `json:"segs"` // percent of the day, state
}

// ScheduleDays lays the published days of a group out as bars.
func ScheduleDays(r schedRegion, group string) []dayLine {
	var out []dayLine
	for _, day := range r.Days {
		iv, ok := day.Groups[group]
		if !ok {
			continue
		}
		states := make([]string, 1440)
		for i := range states {
			states[i] = powerOn
		}
		for _, it := range iv {
			for m := hmMinutes(it.From); m < hmMinutes(it.To); m++ {
				if it.Type == powerOff || states[m] == powerOn {
					states[m] = it.Type
				}
			}
		}
		line := dayLine{Date: day.Date}
		start := 0
		for m := 1; m <= 1440; m++ {
			if m == 1440 || states[m] != states[start] {
				line.Segs = append(line.Segs, [2]string{strconv.FormatFloat(float64(m-start)/14.4, 'f', 2, 64), states[start]})
				start = m
			}
		}
		out = append(out, line)
	}
	return out
}

// LastRun returns the last stretch of power.csv without a gap longer than
// ten minutes: the draw and the charge of the latest run on battery.
func LastRun(powerCSV string, loc *time.Location) ([]drawPoint, string) {
	rows := csvRows(powerCSV)
	var pts []drawPoint
	var prev time.Time
	day := ""
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", r[0], loc)
		w, err2 := strconv.ParseFloat(r[2], 64)
		if err != nil || err2 != nil || w <= 0 {
			continue
		}
		if !prev.IsZero() && t.Sub(prev) > 10*time.Minute {
			pts = pts[:0]
		}
		if len(pts) == 0 {
			day = t.Format("2006-01-02")
		}
		pct, _ := strconv.Atoi(r[3])
		pts = append(pts, drawPoint{T: t.Format("15:04"), Watts: w, Pct: pct})
		prev = t
	}
	if len(pts) < 5 {
		return nil, ""
	}
	// at most 240 points, so that a long night does not flood the page
	if step := (len(pts) + 239) / 240; step > 1 {
		var thin []drawPoint
		for i := 0; i < len(pts); i += step {
			end := i + step
			if end > len(pts) {
				end = len(pts)
			}
			sum := 0.0
			for _, p := range pts[i:end] {
				sum += p.Watts
			}
			thin = append(thin, drawPoint{T: pts[i].T, Watts: sum / float64(end-i), Pct: pts[end-1].Pct})
		}
		pts = thin
	}
	return pts, day
}

// SeedDraws works out the remembered draw of every kind of work from the
// newest two hours of each in power.csv.
func SeedDraws(powerCSV string) map[string]float64 {
	rows := csvRows(powerCSV)
	out := map[string]float64{}
	seen := map[string]int{}
	type acc struct {
		sum float64
		n   int
	}
	sums := map[string]*acc{}
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if len(r) < 5 {
			continue
		}
		w, err := strconv.ParseFloat(r[2], 64)
		if err != nil || w < 1 || w > 300 {
			continue
		}
		k := DrawKey(r[1], r[4])
		if seen[k] >= 120 {
			continue
		}
		seen[k]++
		if sums[k] == nil {
			sums[k] = &acc{}
		}
		sums[k].sum += w
		sums[k].n++
	}
	for k, a := range sums {
		if a.n >= 5 {
			out[k] = a.sum / float64(a.n)
		}
	}
	return out
}
