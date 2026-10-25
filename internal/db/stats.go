package db

import (
	"fmt"
	"sort"
	"time"
)

// DaySegment is the part of a session that falls within one day.
type DaySegment struct {
	Session
	SegmentStart time.Time `json:"segment_start"`
	SegmentEnd   time.Time `json:"segment_end"`
}

type DaySummary struct {
	Date         string       `json:"date"` // YYYY-MM-DD
	DayOfWeek    int          `json:"day_of_week"`
	DayName      string       `json:"day_name"`
	DayNumber    int          `json:"day_number"`
	WeekNumber   int          `json:"week_number"`
	TotalSeconds int          `json:"total_seconds"`
	Sessions     []DaySegment `json:"sessions"`
}

type MonthData struct {
	Year         int          `json:"year"`
	Month        int          `json:"month"`
	MonthName    string       `json:"month_name"`
	TotalSeconds int          `json:"total_seconds"`
	Days         []DaySummary `json:"days"`
	Projects     []Project    `json:"projects"`
}

type ProjectStat struct {
	ProjectName     string  `json:"project_name"`
	Color           string  `json:"color"`
	TotalSeconds    int     `json:"total_seconds"`
	SessionCount    int     `json:"session_count"`
	Percentage      float64 `json:"percentage"`
	AverageDuration int     `json:"average_duration_seconds"`
}

type YearlyBar struct {
	Year         int    `json:"year"`
	Label        string `json:"label"`
	TotalSeconds int    `json:"total_seconds"`
}

type MonthlyBar struct {
	Label        string `json:"label"` // YYYY-MM
	TotalSeconds int    `json:"total_seconds"`
}

type Stats struct {
	TotalSeconds        int           `json:"total_seconds"`
	SessionCount        int           `json:"session_count"`
	ActiveDaysCount     int           `json:"active_days_count"`
	AveragePerDay       int           `json:"average_per_day_seconds"`
	PomodoroCount       int           `json:"pomodoro_count"`
	FreeCount           int           `json:"free_count"`
	Projects            []ProjectStat `json:"projects"`
	YearlyBreakdown     []YearlyBar   `json:"yearly_breakdown"`
	MonthlyBreakdown    []MonthlyBar  `json:"monthly_breakdown"`
	WeekdayDistribution []int         `json:"weekday_distribution"` // Mon..Sun, seconds
	HourlyDistribution  []int         `json:"hourly_distribution"`  // 0..23, seconds
}

type PeriodOption struct {
	Year  int    `json:"year"`
	Month int    `json:"month,omitempty"`
	Label string `json:"label"`
}

// clip returns the intersection of [start, end) and [from, to).
func clip(start, end, from, to time.Time) (time.Time, time.Time, bool) {
	if start.Before(from) {
		start = from
	}
	if end.After(to) {
		end = to
	}
	return start, end, end.After(start)
}

// splitByHour calls fn for each piece of [start, end) cut at local hour boundaries.
func splitByHour(start, end time.Time, fn func(pieceStart time.Time, seconds int)) {
	for t := start; t.Before(end); {
		next := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.Local)
		if !next.After(t) { // DST edge: never loop in place
			next = t.Add(time.Hour)
		}
		if next.After(end) {
			next = end
		}
		fn(t, int(next.Sub(t).Seconds()))
		t = next
	}
}

// GetMonthData returns the day-by-day timeline of a month. Sessions crossing
// midnight are split into one segment per day.
func (d *DB) GetMonthData(year, month int) (*MonthData, error) {
	from, to := monthBounds(year, month)
	sessions, err := d.SessionsOverlapping(from, to)
	if err != nil {
		return nil, err
	}
	projects, err := d.GetMonthProjects(year, month)
	if err != nil {
		return nil, err
	}

	data := &MonthData{
		Year:      year,
		Month:     month,
		MonthName: fmt.Sprintf("%s %d", from.Month(), year),
		Projects:  projects,
	}
	for day := from; day.Before(to); day = day.AddDate(0, 0, 1) {
		_, week := day.ISOWeek()
		data.Days = append(data.Days, DaySummary{
			Date:       day.Format("2006-01-02"),
			DayOfWeek:  int(day.Weekday()),
			DayName:    day.Weekday().String()[:3],
			DayNumber:  day.Day(),
			WeekNumber: week,
			Sessions:   []DaySegment{},
		})
	}

	for _, s := range sessions {
		for i := range data.Days {
			dayStart := time.Date(year, time.Month(month), data.Days[i].DayNumber, 0, 0, 0, 0, time.Local)
			segStart, segEnd, ok := clip(s.StartTime, s.EndTime, dayStart, dayStart.AddDate(0, 0, 1))
			if !ok {
				continue
			}
			secs := int(segEnd.Sub(segStart).Seconds())
			data.Days[i].Sessions = append(data.Days[i].Sessions, DaySegment{Session: s, SegmentStart: segStart, SegmentEnd: segEnd})
			data.Days[i].TotalSeconds += secs
			data.TotalSeconds += secs
		}
	}
	return data, nil
}

// GetStats aggregates the sessions of a period: "month" (default), "year" or "all".
// Durations are clipped to the period and attributed to the hours they cover.
func (d *DB) GetStats(period string, year, month int) (*Stats, error) {
	now := time.Now()
	if year <= 0 {
		year = now.Year()
	}
	if month < 1 || month > 12 {
		month = int(now.Month())
	}

	var from, to time.Time
	var trendEnd time.Time // the 12-month trend ends with this month
	switch period {
	case "year":
		from = time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
		to = from.AddDate(1, 0, 0)
		trendEnd = time.Date(year, 12, 1, 0, 0, 0, 0, time.Local)
	case "all":
		from = time.Time{}
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local)
		trendEnd = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	default:
		from, to = monthBounds(year, month)
		trendEnd = from
	}
	trendFrom := trendEnd.AddDate(0, -11, 0)
	trendTo := trendEnd.AddDate(0, 1, 0)

	sessions, err := d.GetAllSessions()
	if err != nil {
		return nil, err
	}

	type projAccum struct {
		color string
		secs  int
		count int
	}
	projects := make(map[string]*projAccum)
	activeDays := make(map[string]bool)
	monthly := make(map[string]int)
	yearly := make(map[int]int)
	stats := &Stats{
		WeekdayDistribution: make([]int, 7),
		HourlyDistribution:  make([]int, 24),
	}

	for _, s := range sessions {
		splitByHour(s.StartTime, s.EndTime, func(t time.Time, secs int) {
			yearly[t.Year()] += secs
			if !t.Before(trendFrom) && t.Before(trendTo) {
				monthly[t.Format("2006-01")] += secs
			}
		})

		start, end, ok := clip(s.StartTime, s.EndTime, from, to)
		if !ok {
			continue
		}
		stats.SessionCount++
		if s.Mode == ModeFree {
			stats.FreeCount++
		} else {
			stats.PomodoroCount++
		}
		acc := projects[s.ProjectName]
		if acc == nil {
			acc = &projAccum{color: s.ProjectColor}
			projects[s.ProjectName] = acc
		}
		acc.count++
		splitByHour(start, end, func(t time.Time, secs int) {
			stats.TotalSeconds += secs
			acc.secs += secs
			stats.HourlyDistribution[t.Hour()] += secs
			stats.WeekdayDistribution[(int(t.Weekday())+6)%7] += secs // Monday first
			activeDays[t.Format("2006-01-02")] = true
		})
	}

	stats.Projects = []ProjectStat{}
	for name, acc := range projects {
		pct := 0.0
		if stats.TotalSeconds > 0 {
			pct = float64(acc.secs) / float64(stats.TotalSeconds) * 100
		}
		stats.Projects = append(stats.Projects, ProjectStat{
			ProjectName:     name,
			Color:           acc.color,
			TotalSeconds:    acc.secs,
			SessionCount:    acc.count,
			Percentage:      pct,
			AverageDuration: acc.secs / acc.count,
		})
	}
	sort.Slice(stats.Projects, func(i, j int) bool {
		a, b := stats.Projects[i], stats.Projects[j]
		if a.TotalSeconds != b.TotalSeconds {
			return a.TotalSeconds > b.TotalSeconds
		}
		return a.ProjectName < b.ProjectName
	})

	stats.ActiveDaysCount = len(activeDays)
	if stats.ActiveDaysCount > 0 {
		stats.AveragePerDay = stats.TotalSeconds / stats.ActiveDaysCount
	}

	for m := trendFrom; m.Before(trendTo); m = m.AddDate(0, 1, 0) {
		key := m.Format("2006-01")
		stats.MonthlyBreakdown = append(stats.MonthlyBreakdown, MonthlyBar{Label: key, TotalSeconds: monthly[key]})
	}

	// At least the five years up to the current one, plus any recorded year.
	firstYear, lastYear := now.Year()-4, now.Year()
	for y := range yearly {
		firstYear = min(firstYear, y)
		lastYear = max(lastYear, y)
	}
	for y := firstYear; y <= lastYear; y++ {
		stats.YearlyBreakdown = append(stats.YearlyBreakdown, YearlyBar{Year: y, Label: fmt.Sprint(y), TotalSeconds: yearly[y]})
	}
	return stats, nil
}

// GetAvailablePeriods lists the years and months containing sessions (local
// time), newest first. The current month and year are always included.
func (d *DB) GetAvailablePeriods() ([]int, []PeriodOption, error) {
	sessions, err := d.GetAllSessions()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	monthSet := map[time.Time]bool{time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local): true}
	for _, s := range sessions {
		for m := time.Date(s.StartTime.Year(), s.StartTime.Month(), 1, 0, 0, 0, 0, time.Local); m.Before(s.EndTime); m = m.AddDate(0, 1, 0) {
			monthSet[m] = true
		}
	}

	monthStarts := make([]time.Time, 0, len(monthSet))
	for m := range monthSet {
		monthStarts = append(monthStarts, m)
	}
	sort.Slice(monthStarts, func(i, j int) bool { return monthStarts[i].After(monthStarts[j]) })

	years := []int{}
	months := []PeriodOption{}
	for _, m := range monthStarts {
		months = append(months, PeriodOption{Year: m.Year(), Month: int(m.Month()), Label: fmt.Sprintf("%s %d", m.Month(), m.Year())})
		if len(years) == 0 || years[len(years)-1] != m.Year() {
			years = append(years, m.Year())
		}
	}
	return years, months, nil
}
