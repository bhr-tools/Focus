package db

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/bhr-tools/kit/auth"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test_focus.db"))
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func mustRecord(t *testing.T, d *DB, project string, start time.Time, dur time.Duration) *Session {
	t.Helper()
	s, err := d.RecordSession(Session{ProjectName: project, StartTime: start, EndTime: start.Add(dur), Mode: ModePomodoro, Completed: true})
	if err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	return s
}

func TestSessionCRUD(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now().Truncate(time.Second)

	sess, err := database.RecordSession(Session{
		ProjectName:     "DeepWork",
		StartTime:       now.Add(-25 * time.Minute),
		EndTime:         now,
		DurationSeconds: 1500,
		Completed:       true,
		Mode:            ModePomodoro,
	})
	if err != nil {
		t.Fatalf("RecordSession failed: %v", err)
	}
	if sess.ID == 0 || sess.UUID == "" {
		t.Fatalf("expected ID and UUID, got %+v", sess)
	}

	all, err := database.GetAllSessions()
	if err != nil || len(all) != 1 || all[0].ProjectName != "deepwork" || !all[0].Completed {
		t.Fatalf("unexpected sessions %+v (err %v)", all, err)
	}

	updated, err := database.UpdateSession(sess.ID, "DeepWork Refined", now.Add(-30*time.Minute), now)
	if err != nil {
		t.Fatalf("UpdateSession failed: %v", err)
	}
	if updated.ProjectName != "deepwork refined" || updated.DurationSeconds != 1800 || updated.UUID != sess.UUID {
		t.Errorf("unexpected updated session: %+v", updated)
	}

	if _, err := database.UpdateSession(9999, "X", now, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound updating a missing session, got %v", err)
	}

	if err := database.DeleteSession(fmt.Sprint(sess.ID)); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	if err := database.DeleteSession(fmt.Sprint(sess.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound deleting twice, got %v", err)
	}

	other := mustRecord(t, database, "ByUUID", now.Add(-time.Hour), time.Minute)
	if err := database.DeleteSession(other.UUID); err != nil {
		t.Fatalf("DeleteSession by UUID failed: %v", err)
	}
	if all, _ := database.GetAllSessions(); len(all) != 0 {
		t.Fatalf("expected 0 sessions after delete, got %d", len(all))
	}
}

func TestRecordSessionUpsertsOnUUID(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now().Truncate(time.Second)
	first, _ := database.RecordSession(Session{UUID: "fixed-uuid", ProjectName: "A", EndTime: now, DurationSeconds: 60})
	second, err := database.RecordSession(Session{UUID: "fixed-uuid", ProjectName: "B", EndTime: now, DurationSeconds: 120})
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("expected the same row, got ids %d and %d", first.ID, second.ID)
	}
	all, _ := database.GetAllSessions()
	if len(all) != 1 || all[0].ProjectName != "b" || all[0].DurationSeconds != 120 {
		t.Fatalf("unexpected sessions after upsert: %+v", all)
	}
}

func TestLegacySchemaMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy_focus.db")

	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create legacy sqlite db: %v", err)
	}
	stmts := []string{
		`CREATE TABLE sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_name TEXT NOT NULL,
			mode TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME NOT NULL,
			duration_seconds INTEGER NOT NULL,
			completed INTEGER NOT NULL DEFAULT 1,
			note TEXT DEFAULT ''
		)`,
		`CREATE TABLE auth_sessions (token TEXT PRIMARY KEY, created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL)`,
		`INSERT INTO sessions (project_name, mode, start_time, end_time, duration_seconds, completed, note)
		 VALUES ('LegacyProj', 'pomodoro', '2026-01-01T10:00:00Z', '2026-01-01T10:20:00Z', 1200, 1, NULL)`,
	}
	for _, q := range stmts {
		if _, err := legacy.Exec(q); err != nil {
			legacy.Close()
			t.Fatalf("legacy setup %q: %v", q, err)
		}
	}
	legacy.Close()

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed on legacy database: %v", err)
	}
	defer database.Close()

	var cols int
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name IN ('uuid', 'updated_at')`).Scan(&cols); err != nil || cols != 2 {
		t.Fatalf("expected uuid and updated_at columns, got %d (err %v)", cols, err)
	}
	all, err := database.GetAllSessions()
	if err != nil || len(all) != 1 {
		t.Fatalf("legacy session unreadable: %v (err %v)", all, err)
	}
	if all[0].UUID == "" || all[0].UpdatedAt.IsZero() || all[0].Note != "" {
		t.Errorf("legacy session not backfilled: %+v", all[0])
	}
	var n int
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('auth_sessions') WHERE name = 'expires_at'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("legacy auth_sessions table should be replaced by the kit's")
	}
	if _, err := database.RecordSession(Session{ProjectName: "NewOnMigrated", EndTime: time.Now(), DurationSeconds: 60}); err != nil {
		t.Fatalf("RecordSession failed on migrated db: %v", err)
	}
}

func TestRecordSessionDurationHandling(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now()

	cases := []struct {
		name       string
		start, end time.Time
		duration   int
		want       int
	}{
		{"from interval", now.Add(-60 * time.Second), now, 0, 60},
		{"empty interval", now, now, 0, 1},
		{"inverted interval", now, now.Add(-10 * time.Second), -5, 1},
		{"explicit duration wins", now.Add(-time.Hour), now, 90, 90},
	}
	for _, c := range cases {
		s, err := database.RecordSession(Session{ProjectName: c.name, StartTime: c.start, EndTime: c.end, DurationSeconds: c.duration})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if s.DurationSeconds != c.want {
			t.Errorf("%s: expected %ds, got %d", c.name, c.want, s.DurationSeconds)
		}
		if got := int(s.EndTime.Sub(s.StartTime).Seconds()); got != s.DurationSeconds {
			t.Errorf("%s: interval %ds does not match duration %ds", c.name, got, s.DurationSeconds)
		}
	}
}

func TestProjectsAreCaseInsensitiveWithStableColors(t *testing.T) {
	database := setupTestDB(t)
	a, _ := database.GetOrCreateProject("Maths")
	b, _ := database.GetOrCreateProject("  maths ")
	c, _ := database.GetOrCreateProject("Physics")
	if a.ID != b.ID || a.Name != "maths" || b.Name != "maths" {
		t.Errorf("expected the same project, got %+v and %+v", a, b)
	}
	if a.Color == c.Color || c.ColorIndex != a.ColorIndex+1 {
		t.Errorf("expected consecutive palette colors, got %+v and %+v", a, c)
	}
	if blank, _ := database.GetOrCreateProject("   "); blank.Name != DefaultProjectName {
		t.Errorf("blank names should map to %q, got %q", DefaultProjectName, blank.Name)
	}
}

func TestRenameProjectMergesEverywhere(t *testing.T) {
	database := setupTestDB(t)
	jan := time.Date(2026, 1, 10, 9, 0, 0, 0, time.Local)
	feb := time.Date(2026, 2, 10, 9, 0, 0, 0, time.Local)
	mustRecord(t, database, "Math", jan, time.Hour)
	mustRecord(t, database, "Math", feb, time.Hour)
	mustRecord(t, database, "Maths", feb.Add(2*time.Hour), time.Hour)

	name, err := database.RenameProject("Math", "MATHS")
	if err != nil || name != "maths" {
		t.Fatalf("merge into existing project: got %q, %v", name, err)
	}
	stats, _ := database.GetStats("all", 0, 0)
	if len(stats.Projects) != 1 || stats.Projects[0].ProjectName != "maths" || stats.Projects[0].TotalSeconds != 3*3600 {
		t.Fatalf("expected a single merged project, got %+v", stats.Projects)
	}
	projects, _ := database.ListProjects()
	for _, p := range projects {
		if p.Name == "math" {
			t.Fatal("the merged project must be deleted")
		}
	}

	// A change of case only is a no-op: names are always lowercase.
	if name, err := database.RenameProject("maths", "Maths"); err != nil || name != "maths" {
		t.Fatalf("case rename: got %q, %v", name, err)
	}
}

func TestLegacyProjectNamesAreLowercased(t *testing.T) {
	database := setupTestDB(t)
	mustRecord(t, database, "physique", time.Date(2026, 1, 10, 9, 0, 0, 0, time.Local), time.Hour)
	legacy := []string{
		`INSERT INTO projects (name, color, color_index, created_at) VALUES ('Maths', '#000', 5, '2026-01-01T00:00:00Z')`,
		`INSERT INTO projects (name, color, color_index, created_at) VALUES ('Été', '#111', 6, '2026-01-01T00:00:00Z')`,
		`INSERT INTO projects (name, color, color_index, created_at) VALUES ('été', '#222', 7, '2026-01-01T00:00:00Z')`,
		`UPDATE sessions SET project_name = 'PHYSIQUE'`,
		`INSERT INTO sessions (uuid, project_name, mode, start_time, end_time, duration_seconds)
			VALUES ('a', 'Maths', 'pomodoro', '2026-01-11T09:00:00Z', '2026-01-11T10:00:00Z', 3600),
			       ('b', 'Été', 'pomodoro', '2026-01-12T09:00:00Z', '2026-01-12T10:00:00Z', 3600)`,
	}
	for _, q := range legacy {
		if _, err := database.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.lowercaseProjects(); err != nil {
		t.Fatal(err)
	}

	projects, _ := database.ListProjects()
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "maths,physique,été" {
		t.Fatalf("expected lowercase projects with été merged, got %v", names)
	}
	sessions, _ := database.GetAllSessions()
	for _, s := range sessions {
		if s.ProjectName != strings.ToLower(s.ProjectName) {
			t.Fatalf("session not lowercased: %+v", s)
		}
	}
}

func TestRenameProjectForMonthKeepsOtherMonths(t *testing.T) {
	database := setupTestDB(t)
	mustRecord(t, database, "Draft", time.Date(2026, 1, 10, 9, 0, 0, 0, time.Local), time.Hour)
	mustRecord(t, database, "Draft", time.Date(2026, 2, 10, 9, 0, 0, 0, time.Local), time.Hour)

	if _, err := database.RenameProjectForMonth(2026, 2, "Draft", "Final"); err != nil {
		t.Fatal(err)
	}
	jan, _ := database.GetMonthData(2026, 1)
	feb, _ := database.GetMonthData(2026, 2)
	if jan.Projects[0].Name != "draft" || feb.Projects[0].Name != "final" {
		t.Fatalf("expected Draft in January and Final in February, got %+v / %+v", jan.Projects, feb.Projects)
	}
}

func TestMonthDataSplitsSessionsAtMidnight(t *testing.T) {
	database := setupTestDB(t)
	start := time.Date(2026, 3, 10, 23, 0, 0, 0, time.Local)
	mustRecord(t, database, "Night", start, 2*time.Hour)

	month, err := database.GetMonthData(2026, 3)
	if err != nil {
		t.Fatalf("GetMonthData: %v", err)
	}
	day10, day11 := month.Days[9], month.Days[10]
	if day10.TotalSeconds != 3600 || day11.TotalSeconds != 3600 || month.TotalSeconds != 7200 {
		t.Fatalf("expected 1h + 1h, got %d + %d (month %d)", day10.TotalSeconds, day11.TotalSeconds, month.TotalSeconds)
	}
	if len(day11.Sessions) != 1 || !day11.Sessions[0].SegmentStart.Equal(time.Date(2026, 3, 11, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("expected a segment starting at midnight on the 11th, got %+v", day11.Sessions)
	}
	if day11.Sessions[0].DurationSeconds != 7200 {
		t.Errorf("segments must keep the full session for editing")
	}
	if len(month.Projects) != 1 || month.Projects[0].Name != "night" {
		t.Errorf("unexpected month projects %+v", month.Projects)
	}
}

func TestMonthBoundarySessionBelongsToBothMonths(t *testing.T) {
	database := setupTestDB(t)
	mustRecord(t, database, "Edge", time.Date(2026, 3, 31, 23, 30, 0, 0, time.Local), time.Hour)

	march, _ := database.GetMonthData(2026, 3)
	april, _ := database.GetMonthData(2026, 4)
	if march.TotalSeconds != 1800 || april.TotalSeconds != 1800 {
		t.Fatalf("expected 30 min in each month, got %d and %d", march.TotalSeconds, april.TotalSeconds)
	}

	stats, err := database.GetStats("month", 2026, 4)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalSeconds != 1800 || stats.HourlyDistribution[0] != 1800 || stats.SessionCount != 1 {
		t.Fatalf("unexpected April stats: total %d, 0h %d, count %d", stats.TotalSeconds, stats.HourlyDistribution[0], stats.SessionCount)
	}
	if last := stats.MonthlyBreakdown[len(stats.MonthlyBreakdown)-1]; last.Label != "2026-04" || last.TotalSeconds != 1800 {
		t.Fatalf("unexpected trend tail %+v", last)
	}
}

func TestStatsAggregation(t *testing.T) {
	database := setupTestDB(t)
	day := time.Date(2026, 5, 4, 9, 0, 0, 0, time.Local) // a Monday
	mustRecord(t, database, "A", day, 2*time.Hour)
	mustRecord(t, database, "B", day.Add(3*time.Hour), time.Hour)
	mustRecord(t, database, "A", day.AddDate(0, 0, 1), time.Hour)
	if _, err := database.RecordSession(Session{ProjectName: "B", Mode: ModeFree, EndTime: day.AddDate(0, 0, 2).Add(time.Hour), DurationSeconds: 3600}); err != nil {
		t.Fatal(err)
	}

	stats, err := database.GetStats("month", 2026, 5)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalSeconds != 5*3600 || stats.SessionCount != 4 || stats.ActiveDaysCount != 3 {
		t.Fatalf("unexpected totals %+v", stats)
	}
	if stats.PomodoroCount != 3 || stats.FreeCount != 1 {
		t.Errorf("unexpected mode counts %d/%d", stats.PomodoroCount, stats.FreeCount)
	}
	if stats.Projects[0].ProjectName != "a" || stats.Projects[0].TotalSeconds != 3*3600 || stats.Projects[0].AverageDuration != 5400 {
		t.Errorf("unexpected top project %+v", stats.Projects[0])
	}
	if stats.WeekdayDistribution[0] != 3*3600 || stats.HourlyDistribution[9] != 3*3600 || stats.HourlyDistribution[10] != 3600 {
		t.Errorf("unexpected distributions %v %v", stats.WeekdayDistribution, stats.HourlyDistribution)
	}

	all, _ := database.GetStats("all", 0, 0)
	year, _ := database.GetStats("year", 2026, 0)
	if all.TotalSeconds != stats.TotalSeconds || year.TotalSeconds != stats.TotalSeconds || len(year.MonthlyBreakdown) != 12 {
		t.Errorf("all/year totals differ: %d %d", all.TotalSeconds, year.TotalSeconds)
	}
}

func TestImportDeduplicatesAndSkipsInvalid(t *testing.T) {
	database := setupTestDB(t)
	csvData := "Project,Start Time,End Time,Duration (seconds),Note\n" +
		"Maths,2026-01-05T09:00:00Z,2026-01-05T10:00:00Z,3600,algebra\n" +
		"Maths,2026-01-05T11:00:00Z,,0,\n" + // no end and no duration: invalid
		"Dev,2026-01-06 09:00:00,2026-01-06 09:30:00,,\n"
	sessions, err := ParseImportSessions([]byte(csvData))
	if err != nil {
		t.Fatalf("ParseImportSessions: %v", err)
	}
	imported, skipped, err := database.ImportSessions(sessions)
	if err != nil || imported != 2 || skipped != 1 {
		t.Fatalf("first import: %d imported, %d skipped (err %v)", imported, skipped, err)
	}
	imported, skipped, _ = database.ImportSessions(sessions)
	if imported != 0 || skipped != 3 {
		t.Fatalf("re-import should skip everything, got %d imported, %d skipped", imported, skipped)
	}

	// A Focus export round-trips without duplicates (UUIDs match).
	var buf bytes.Buffer
	if err := database.WriteExport(&buf, FormatJSON); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseImportSessions(buf.Bytes())
	if err != nil || len(parsed) != 2 {
		t.Fatalf("parse export: %v (%d sessions)", err, len(parsed))
	}
	if imported, _, _ := database.ImportSessions(parsed); imported != 0 {
		t.Fatalf("export round-trip created %d duplicates", imported)
	}

	dev := mustFind(t, database, "dev")
	if want := time.Date(2026, 1, 6, 9, 0, 0, 0, time.Local); !dev.StartTime.Equal(want) {
		t.Errorf("zoneless CSV times must be local: got %v, want %v", dev.StartTime, want)
	}
}

func TestSessionDurationIsBounded(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now()
	if _, err := database.RecordSession(Session{ProjectName: "X", EndTime: now, DurationSeconds: 8 * 24 * 3600}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected ErrInvalidSession, got %v", err)
	}
	// Extreme bounds must not overflow into an accepted negative duration.
	huge := Session{ProjectName: "X", StartTime: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), EndTime: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)}
	if imported, skipped, err := database.ImportSessions([]Session{huge}); err != nil || imported != 0 || skipped != 1 {
		t.Fatalf("huge session should be skipped: %d imported, %d skipped (err %v)", imported, skipped, err)
	}
}

func TestParseTimewarrior(t *testing.T) {
	data := `[{"id":1,"start":"20260105T090000Z","end":"20260105T100000Z","tags":["Maths","x"]},
	          {"id":2,"start":"20260105T110000Z","tags":["Open"]}]`
	sessions, err := ParseImportSessions([]byte(data))
	if err != nil || len(sessions) != 2 {
		t.Fatalf("parse: %v (%d)", err, len(sessions))
	}
	if sessions[0].ProjectName != "Maths" || sessions[0].EndTime.Sub(sessions[0].StartTime) != time.Hour {
		t.Errorf("unexpected entry %+v", sessions[0])
	}
	database := setupTestDB(t)
	if imported, skipped, _ := database.ImportSessions(sessions); imported != 1 || skipped != 1 {
		t.Errorf("open intervals must be skipped: %d imported, %d skipped", imported, skipped)
	}
}

func mustFind(t *testing.T, d *DB, project string) Session {
	t.Helper()
	all, _ := d.GetAllSessions()
	for _, s := range all {
		if s.ProjectName == project {
			return s
		}
	}
	t.Fatalf("no session for %s", project)
	return Session{}
}

func TestSettingsArePrivate(t *testing.T) {
	database := setupTestDB(t)
	if err := database.SetSetting("password_hash", "x"); err == nil {
		t.Fatal("password_hash must not be writable as a public setting")
	}
	settings, _ := database.GetSettings()
	for k := range settings {
		if strings.Contains(k, "password") {
			t.Fatalf("settings leak %q", k)
		}
	}
	if settings[SettingWorkDuration] != "60" {
		t.Errorf("missing default work duration: %v", settings)
	}
}

// A database of the last release before the kit: its password (current and
// first hash formats), logins and connected apps work through kit/auth.
func TestAuthMovesToKit(t *testing.T) {
	for _, tc := range []struct{ name, hash string }{
		{"pbkdf2", ""},
		{"first", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "focus.db")
			old, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			salt := []byte("0123456789abcdef")
			var hash string
			if tc.name == "pbkdf2" {
				key, _ := pbkdf2.Key(sha256.New, "secret", salt, 1000, 32)
				hash = fmt.Sprintf("pbkdf2-sha256$1000$%x$%x", salt, key)
			} else {
				sum := sha256.Sum256(append(append([]byte{}, salt...), "secret"...))
				hash = fmt.Sprintf("%x$%x", salt, sum)
			}
			hashOf := func(token string) string { sum := sha256.Sum256([]byte(token)); return hex.EncodeToString(sum[:]) }
			now := time.Now().Unix()
			for _, q := range []string{
				`CREATE TABLE sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, uuid TEXT UNIQUE, project_name TEXT NOT NULL COLLATE NOCASE, mode TEXT NOT NULL,
					start_time DATETIME NOT NULL, end_time DATETIME NOT NULL, duration_seconds INTEGER NOT NULL, completed INTEGER NOT NULL DEFAULT 1,
					note TEXT DEFAULT '', updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
				`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
				`CREATE TABLE auth_tokens (token_hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL)`,
				`CREATE TABLE app_tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL, created_at INTEGER NOT NULL, used_at INTEGER)`,
				fmt.Sprintf(`INSERT INTO settings VALUES ('password_hash', '%s'), ('work_duration', '45')`, hash),
				fmt.Sprintf(`INSERT INTO auth_tokens VALUES ('%s', %d), ('%s', %d)`, hashOf("browser"), now+3600, hashOf("expired"), now-10),
				fmt.Sprintf(`INSERT INTO app_tokens (name, token_hash, created_at, used_at) VALUES ('Project', '%s', %d, NULL)`, hashOf("project"), now-86400),
			} {
				if _, err := old.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			old.Close()

			database, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			a, err := auth.New(database.SQL(), "focus_session", false)
			if err != nil {
				t.Fatal(err)
			}
			if ok, _ := a.VerifyPassword("wrong"); ok {
				t.Fatal("wrong password accepted")
			}
			if ok, err := a.VerifyPassword("secret"); !ok {
				t.Fatalf("password lost (%v)", err)
			}
			if !a.ValidateSession("browser") || a.ValidateSession("expired") {
				t.Fatal("logins not carried over")
			}
			r := httptest.NewRequest("GET", "/api/month", nil)
			r.Header.Set("Authorization", "Bearer project")
			if tok := a.RequestAppToken(r); tok == nil || tok.Name != "Project" || tok.Scope != "read" {
				t.Fatalf("connected app lost: %+v", tok)
			}
			if settings, _ := database.GetSettings(); settings[SettingWorkDuration] != "45" {
				t.Fatalf("settings lost: %v", settings)
			}
			var n int
			database.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('auth_tokens', 'app_tokens')`).Scan(&n)
			database.conn.QueryRow(`SELECT COUNT(*) + ? FROM settings WHERE key = 'password_hash'`, n).Scan(&n)
			if n != 0 {
				t.Fatal("old auth rows left behind")
			}
		})
	}
}
