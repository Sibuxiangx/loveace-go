// Package calendar materialises the academic calendar (DESIGN.md §11.1):
// term + day rules → DayInfo per date, so a date lookup is O(1) and a
// range query is a slice. Day kinds: teaching (default), holiday,
// makeup (调休, carrying which week's weekday timetable this day
// follows), exam, vacation.
package calendar

import (
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// Day kinds.
const (
	KindTeaching = "teaching"
	KindHoliday  = "holiday"
	KindMakeup   = "makeup"
	KindExam     = "exam"
	KindVacation = "vacation"
)

// DayInfo is one materialised calendar day.
type DayInfo struct {
	Date    string  `json:"date"` // YYYY-MM-DD
	WeekNo  int     `json:"week_no"`
	Weekday int     `json:"weekday"` // 1=Monday … 7=Sunday
	Kind    string  `json:"kind"`
	Follows *Follow `json:"follows,omitempty"`
}

// Follow names the week/weekday whose timetable this makeup day follows.
type Follow struct {
	WeekNo  int `json:"week_no"`
	Weekday int `json:"weekday"`
}

// Materialise renders [from, to] (inclusive) for the term. Days outside
// the term's week span still render (week_no clamps to the nearest side,
// kind from the rules if any) — callers decide how to display them.
func Materialise(term store.CalendarTerm, rules []store.CalendarRule, from, to time.Time) []DayInfo {
	ruleByDay := map[string]store.CalendarRule{}
	for _, r := range rules {
		ruleByDay[r.Day.Format("2006-01-02")] = r
	}
	monday := term.FirstWeekMonday
	var out []DayInfo
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		day := d
		info := DayInfo{
			Date:    day.Format("2006-01-02"),
			Weekday: weekdayNo(day),
			Kind:    KindTeaching,
		}
		if !day.Before(monday) {
			info.WeekNo = int(day.Sub(monday).Hours()/24/7) + 1
		}
		if rule, ok := ruleByDay[info.Date]; ok {
			info.Kind = rule.Kind
			if rule.Kind == KindMakeup && rule.FollowWeek > 0 && rule.FollowWeekday > 0 {
				info.Follows = &Follow{WeekNo: rule.FollowWeek, Weekday: rule.FollowWeekday}
			}
		}
		out = append(out, info)
	}
	return out
}

func weekdayNo(d time.Time) int {
	wd := int(d.Weekday()) // 0=Sunday
	if wd == 0 {
		return 7
	}
	return wd
}
