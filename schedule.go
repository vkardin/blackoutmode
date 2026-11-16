package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

// The outage schedule comes from the open data of the "Koly Ye" project:
// one JSON file per region, intervals per group, Kyiv time.
const scheduleBaseURL = "https://vkardin.github.io/koly/v1/regions/"

type schedInterval struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"` // "off" or "maybe"
}

type schedDay struct {
	Date   string                     `json:"date"`
	Groups map[string][]schedInterval `json:"groups"`
}

type schedRegion struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	FetchedAt string `json:"fetchedAt"`
	Emergency struct {
		Active bool `json:"active"`
	} `json:"emergency"`
	Days []schedDay `json:"days"`
}

// GroupNames lists the groups (queues) of the region, sorted.
func (r schedRegion) GroupNames() []string {
	seen := map[string]bool{}
	names := []string{}
	for _, d := range r.Days {
		for g := range d.Groups {
			if !seen[g] {
				seen[g] = true
				names = append(names, g)
			}
		}
	}
	sort.Strings(names)
	return names
}

func ParseRegion(data []byte) (schedRegion, error) {
	var r schedRegion
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.ID == "" || r.Days == nil {
		return r, errors.New("not a region file")
	}
	return r, nil
}

func kyiv() *time.Location {
	if loc, err := time.LoadLocation("Europe/Kyiv"); err == nil {
		return loc
	}
	return time.Local
}

func hmMinutes(s string) int {
	h, m, _ := strings.Cut(s, ":")
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	return clamp(hh*60+mm, 0, 1440)
}

const (
	powerOn    = "on"
	powerOff   = "off"
	powerMaybe = "maybe"
)

// schedStatus is what the schedule says about one group at one moment.
type schedStatus struct {
	Known    bool      // the schedule has today's day for this group
	Usable   bool      // fresh data, no emergency outages: safe to act on
	State    string    // powerOn, powerOff or powerMaybe
	ChangeAt time.Time // next change of the state, zero when not known
	Next     string    // the state after the change
	NextOff  time.Time // start of the next sure outage, zero when none is known
}

// ScheduleStatus reads the state of a group at the given moment from today's
// and, when published, tomorrow's schedule.
func ScheduleStatus(r schedRegion, group string, now time.Time) schedStatus {
	now = now.In(kyiv())
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var line []string
	for d := 0; d < 2; d++ {
		date := midnight.AddDate(0, 0, d).Format("2006-01-02")
		var day *schedDay
		for i := range r.Days {
			if r.Days[i].Date == date {
				day = &r.Days[i]
			}
		}
		if day == nil {
			break
		}
		iv, ok := day.Groups[group]
		if !ok {
			break
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
		line = append(line, states...)
	}
	var st schedStatus
	if len(line) == 0 {
		return st
	}
	cur := now.Hour()*60 + now.Minute()
	st.Known, st.State = true, line[cur]
	if t, err := time.Parse(time.RFC3339, r.FetchedAt); err == nil {
		st.Usable = r.Status == "ok" && !r.Emergency.Active && now.Sub(t) < 3*time.Hour
	}
	for m := cur + 1; m < len(line); m++ {
		if st.ChangeAt.IsZero() && line[m] != st.State {
			st.ChangeAt, st.Next = midnight.Add(time.Duration(m)*time.Minute), line[m]
		}
		if line[m] == powerOff && line[m-1] != powerOff {
			st.NextOff = midnight.Add(time.Duration(m) * time.Minute)
			break
		}
	}
	return st
}

// ScheduleLine is the short text for the widget and the menu.
func ScheduleLine(st schedStatus) string {
	if !st.Known {
		return ""
	}
	at := ""
	if !st.ChangeAt.IsZero() {
		at = st.ChangeAt.Format("15:04")
	}
	s := ""
	switch st.State {
	case powerOff:
		s = "по графику света нет"
		if at != "" {
			s += " до ≈" + at
		}
	case powerMaybe:
		s = "возможно отключение"
		if at != "" {
			s += " до ≈" + at
		}
	default:
		switch {
		case !st.NextOff.IsZero():
			s = "отключение ≈" + st.NextOff.Format("15:04")
		case at != "":
			s = "возможно отключение ≈" + at
		default:
			s = "отключений в графике нет"
		}
	}
	if !st.Usable {
		s += " (график устарел)"
	}
	return s
}

// MainsExpected reports whether, by the schedule, the grid should be on now.
// Half an hour of slack is given on both sides of an outage, since real
// switching often differs from the schedule by that much.
func MainsExpected(r schedRegion, group string, now time.Time) bool {
	for _, d := range []time.Duration{-30 * time.Minute, 0, 30 * time.Minute} {
		if st := ScheduleStatus(r, group, now.Add(d)); st.Known && st.State != powerOff {
			return true
		}
	}
	return false
}

// MainsLikely is the stricter question for a laptop that cannot tell its own
// charger from a power bank: the grid is on by the schedule right now, or
// is due back within half an hour. Power that appears soon after an outage
// began is taken for a power bank.
func MainsLikely(r schedRegion, group string, now time.Time) bool {
	for _, d := range []time.Duration{0, 30 * time.Minute} {
		if st := ScheduleStatus(r, group, now.Add(d)); st.Known && st.State != powerOff {
			return true
		}
	}
	return false
}

// kolyPlace reads the main place from the settings of the Koly Ye widget.
func kolyPlace(settings []byte) (region, group string) {
	var s struct {
		Places []struct {
			ID       string `json:"id"`
			RegionID string `json:"regionId"`
			Group    string `json:"group"`
		} `json:"places"`
		Main *string `json:"mainPlaceId"`
	}
	if json.Unmarshal(settings, &s) != nil || len(s.Places) == 0 {
		return "", ""
	}
	p := s.Places[0]
	for _, c := range s.Places {
		if s.Main != nil && c.ID == *s.Main {
			p = c
		}
	}
	return p.RegionID, p.Group
}
