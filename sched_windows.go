//go:build windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type schedCtl struct {
	region  schedRegion
	loaded  bool
	id      string // region the data belongs to
	etag    string
	busy    bool
	lastTry time.Time
	warned  time.Time // outage the user has been warned about
	lifted  time.Time // outage the charge limit has been lifted for
	logged  string
	note    string // the last answer written to the log
}

// schedPlace returns the region and the group: from config.ini, or from the
// settings of the Koly Ye widget when they are not set there.
func (a *app) schedPlace() (string, string) {
	if a.cfg.Region != "" && a.cfg.Group != "" {
		return a.cfg.Region, a.cfg.Group
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), "io.github.vkardin.koly.widget", "settings.json"))
	if err != nil {
		return "", ""
	}
	return kolyPlace(data)
}

func (a *app) schedCachePath() string { return filepath.Join(a.dir, "schedule.json") }

func (a *app) schedSet(data []byte, etag string) bool {
	r, err := ParseRegion(data)
	if err != nil {
		return false
	}
	a.sched.region, a.sched.loaded, a.sched.id = r, true, r.ID
	if etag != "" {
		a.sched.etag = etag
	}
	return true
}

// schedStatus is the schedule for the user's group right now.
func (a *app) schedStatus() schedStatus {
	region, group := a.schedPlace()
	if !a.cfg.Schedule || !a.sched.loaded || region == "" || a.sched.id != region {
		return schedStatus{}
	}
	return ScheduleStatus(a.sched.region, group, time.Now())
}

// fetchSchedule refreshes the schedule at most once in 15 minutes.
func (a *app) fetchSchedule(force bool) {
	region, group := a.schedPlace()
	if key := region + "/" + group; key != a.sched.logged {
		a.sched.logged = key
		a.log("schedule: region=%q group=%q", region, group)
	}
	if !a.cfg.Schedule || region == "" || a.sched.busy || a.st.WifiOff {
		return
	}
	if a.sched.id != region {
		a.sched.loaded, a.sched.etag = false, ""
		if data, err := os.ReadFile(a.schedCachePath()); err == nil {
			if !a.schedSet(data, "") || a.sched.id != region {
				a.sched.loaded = false
			}
		}
		force = time.Since(a.sched.lastTry) > time.Minute
	}
	if !force && time.Since(a.sched.lastTry) < 15*time.Minute {
		return
	}
	a.sched.busy, a.sched.lastTry = true, time.Now()
	etag := a.sched.etag
	go func() {
		var data []byte
		newTag, note := "", ""
		req, err := http.NewRequest("GET", scheduleBaseURL+region+".json", nil)
		if err == nil {
			if etag != "" {
				req.Header.Set("If-None-Match", etag)
			}
			var resp *http.Response
			resp, err = (&http.Client{Timeout: 15 * time.Second}).Do(req)
			if err == nil {
				defer resp.Body.Close()
				switch resp.StatusCode {
				case 200:
					data, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
					newTag = resp.Header.Get("ETag")
				case 304:
					note = "not changed"
				default:
					note = resp.Status
				}
			}
		}
		if err != nil {
			note = err.Error()
		}
		a.post(func() {
			a.sched.busy = false
			if data != nil {
				if r, perr := ParseRegion(data); perr != nil || r.ID != region {
					note = "unexpected answer"
				} else {
					a.schedSet(data, newTag)
					os.WriteFile(a.schedCachePath(), data, 0o644)
					note = "updated, status " + r.Status
				}
			}
			// the data is refreshed many times a day: only a change of the answer is worth a line
			if note != "not changed" && note != a.sched.note {
				a.sched.note = note
				a.log("schedule %s: %s", region, note)
			}
			a.refresh()
		})
	}()
}

// scheduleTick warns before a scheduled outage and gets the battery ready.
func (a *app) scheduleTick() {
	a.fetchSchedule(false)
	st := a.schedStatus()
	if !st.Known || !st.Usable || st.NextOff.IsZero() || st.State == powerOff || a.st.Mode == modeBlackout || !a.onAC() {
		return
	}
	left := time.Until(st.NextOff)
	if a.cfg.Precharge && left < 2*time.Hour && a.limitKnown && a.limitOn && !a.sched.lifted.Equal(st.NextOff) {
		a.sched.lifted = st.NextOff
		a.log("schedule: outage at %s, lifting the charge limit", st.NextOff.Format("15:04"))
		a.toggleChargeLimit()
	}
	if a.cfg.WarnBefore && left < time.Duration(a.cfg.WarnMin)*time.Minute && !a.sched.warned.Equal(st.NextOff) {
		a.sched.warned = st.NextOff
		a.balloon("Скоро отключение по графику",
			fmt.Sprintf("Около %s, возможен сдвиг на полчаса. В батарее %d%%.", st.NextOff.Format("15:04"), a.percent()), false)
	}
}

// mainsBack decides after external power appeared in blackout mode whether
// it is the grid: the laptop's own charger is connected and the schedule
// does not say the outage is still going on. Where the charger cannot be
// told from a power bank, the schedule decides alone, and more strictly.
func (a *app) mainsBack(info map[string]int) {
	if !a.cfg.AutoReturn || a.st.Mode != modeBlackout || !a.onAC() {
		return
	}
	region, group := a.schedPlace()
	st := a.schedStatus()
	_, k1 := info["acfit"]
	_, k2 := info["charge"]
	if k1 && k2 {
		own := info["acfit"] == 1 && info["charge"] == 1
		expected := st.Known && st.Usable && MainsExpected(a.sched.region, group, time.Now())
		a.log("power is back? own charger=%v, schedule known=%v usable=%v expects power=%v (%s/%s)", own, st.Known, st.Usable, expected, region, group)
		if own && expected {
			a.disable("power is back")
		}
		return
	}
	// This laptop does not say which charger is connected: only the schedule is left.
	likely := st.Known && st.Usable && MainsLikely(a.sched.region, group, time.Now())
	a.log("power is back? charger unknown, schedule known=%v usable=%v says power=%v (%s/%s)", st.Known, st.Usable, likely, region, group)
	if likely {
		a.disable("power is back by the schedule")
		a.balloon("Похоже, свет дали", "По графику сейчас должен быть свет, режим выключен. Если это павербанк, включите режим снова.", false)
	}
}

// powerLog keeps one line a minute while the laptop runs on battery, so that
// the effect of the modes can be compared afterwards.
func (a *app) powerLog() {
	avg, ok := a.avgDraw()
	if a.ticks%12 != 0 || !ok || a.onAC() {
		return
	}
	path := filepath.Join(a.dir, "power.csv")
	head := ""
	if _, err := os.Stat(path); err != nil {
		head = "\xef\xbb\xbfвремя;режим;расход, Вт;заряд, %;видеокарта\r\n"
	}
	mode := a.modeKey()
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%s%s;%s;%.1f;%d;%s\r\n", head, time.Now().Format("2006-01-02 15:04"), mode, avg, a.percent(), a.gpu)
		f.Close()
	}
}
