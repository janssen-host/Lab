package backup

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
	_ "time/tzdata"
)

type Schedule struct {
	Frequency string `json:"frequency"`
	Time      string `json:"time,omitempty"`
	KeepLast  int    `json:"keep_last"`
	Day       string `json:"day,omitempty"`
}

var timePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
var days = map[string]time.Weekday{"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday}

func (s Schedule) Validate() error {
	if s.Frequency != "hourly" && s.Frequency != "daily" && s.Frequency != "weekly" {
		return errors.New("frequency must be hourly, daily, or weekly")
	}
	if s.KeepLast < 1 {
		return errors.New("keep_last must be at least 1")
	}
	if s.Frequency != "hourly" && !timePattern.MatchString(s.Time) {
		return errors.New("daily and weekly backups require time in HH:MM format")
	}
	if s.Day != "" {
		if s.Frequency != "weekly" {
			return errors.New("day is only valid for weekly backups")
		}
		if _, ok := days[s.Day]; !ok {
			return errors.New("day must be a lowercase weekday")
		}
	}
	return nil
}
func (s Schedule) Next(after time.Time) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation("Australia/Brisbane")
	if err != nil {
		return time.Time{}, err
	}
	local := after.In(loc)
	if s.Frequency == "hourly" {
		return local.Truncate(time.Hour).Add(time.Hour).UTC(), nil
	}
	hour, _ := strconv.Atoi(s.Time[:2])
	minute, _ := strconv.Atoi(s.Time[3:])
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if s.Frequency == "daily" {
		if !next.After(local) {
			next = next.AddDate(0, 0, 1)
		}
		return next.UTC(), nil
	}
	day := s.Day
	if day == "" {
		day = "monday"
	}
	target, ok := days[day]
	if !ok {
		return time.Time{}, fmt.Errorf("invalid weekday %q", day)
	}
	delta := (int(target) - int(next.Weekday()) + 7) % 7
	next = next.AddDate(0, 0, delta)
	if !next.After(local) {
		next = next.AddDate(0, 0, 7)
	}
	return next.UTC(), nil
}
