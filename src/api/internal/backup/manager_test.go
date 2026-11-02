package backup

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
	_ "modernc.org/sqlite"
)

type masterStub struct{}

func (masterStub) MasterPassword(context.Context, string) (string, error) { return "master", nil }

type dumpStub struct {
	fail  bool
	calls int
}

func (d *dumpStub) Dump(_ context.Context, _ config.Node, _ string, _ string, w io.Writer) error {
	d.calls++
	if d.fail {
		return errors.New("dump failed")
	}
	_, err := w.Write([]byte("PGDMP-test-content"))
	return err
}
func setupManager(t *testing.T) (*Manager, *dumpStub) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE nodes(name TEXT PRIMARY KEY, type TEXT, hostname TEXT, ssh_port INTEGER, configured INTEGER); CREATE TABLE database_hosts(name TEXT PRIMARY KEY,status TEXT); CREATE TABLE databases(id TEXT PRIMARY KEY,node TEXT,name TEXT); CREATE TABLE jobs(id TEXT PRIMARY KEY,type TEXT,node TEXT,database_name TEXT,status TEXT,last_error TEXT,created_at TEXT,updated_at TEXT); INSERT INTO nodes VALUES('db','database','localhost',22,1); INSERT INTO database_hosts VALUES('db','ready'); INSERT INTO databases VALUES('0123456789abcdef0123456789abcdef','db','app')`)
	if err != nil {
		t.Fatal(err)
	}
	d := &dumpStub{}
	m, err := NewManager(db, t.TempDir(), masterStub{}, d)
	if err != nil {
		t.Fatal(err)
	}
	return m, d
}
func TestScheduleTimes(t *testing.T) {
	now := time.Date(2026, 10, 27, 13, 15, 0, 0, time.UTC)
	for _, tc := range []struct {
		s    Schedule
		want string
	}{
		{Schedule{Frequency: "hourly", Time: "ignored", KeepLast: 1}, "2026-10-27T14:00:00Z"},
		{Schedule{Frequency: "daily", Time: "09:30", KeepLast: 2}, "2026-10-27T23:30:00Z"},
		{Schedule{Frequency: "weekly", Time: "09:30", KeepLast: 2}, "2026-11-01T23:30:00Z"},
		{Schedule{Frequency: "weekly", Day: "sunday", Time: "09:30", KeepLast: 2}, "2026-10-31T23:30:00Z"},
	} {
		got, err := tc.s.Next(now)
		if err != nil || got.Format(time.RFC3339) != tc.want {
			t.Errorf("%+v got %v %v want %s", tc.s, got, err, tc.want)
		}
	}
	for _, s := range []Schedule{{Frequency: "monthly", KeepLast: 1}, {Frequency: "daily", Time: "25:00", KeepLast: 1}, {Frequency: "hourly", KeepLast: 0}, {Frequency: "weekly", Day: "funday", Time: "09:00", KeepLast: 1}} {
		if s.Validate() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}
func TestBackupSchedulerRetentionAndFailure(t *testing.T) {
	m, d := setupManager(t)
	ctx := context.Background()
	id := "0123456789abcdef0123456789abcdef"
	start := time.Date(2026, 10, 27, 0, 30, 0, 0, time.UTC)
	s := Schedule{Frequency: "hourly", KeepLast: 2}
	if err := m.Configure(ctx, id, &s, start); err != nil {
		t.Fatal(err)
	}
	for hour := 1; hour <= 3; hour++ {
		if err := m.runDue(ctx, time.Date(2026, 10, 27, hour, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := m.List(ctx, id)
	if err != nil || len(backups) != 2 {
		t.Fatalf("retained backups: %d %v", len(backups), err)
	}
	if d.calls != 3 {
		t.Fatalf("dump count %d", d.calls)
	}
	for _, b := range backups {
		if b.Status != "succeeded" || b.Size == 0 || !strings.HasSuffix(b.Path, ".dump") {
			t.Fatalf("backup %+v", b)
		}
		if _, err := os.Stat(filepath.Join(m.dir, b.Path)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(filepath.Join(m.dir, id))
	if err != nil || len(files) != 2 {
		t.Fatalf("backup files %d: %v", len(files), err)
	}
	d.fail = true
	if err = m.runDue(ctx, time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	backups, err = m.List(ctx, id)
	if err != nil || len(backups) != 3 || backups[0].Status != "failed" {
		t.Fatalf("failure not recorded: %+v %v", backups, err)
	}
	files, err = os.ReadDir(filepath.Join(m.dir, id))
	if err != nil || len(files) != 2 {
		t.Fatalf("failure altered completed backups: %d %v", len(files), err)
	}
	var status string
	if err = m.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, backups[0].JobID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("job %q: %v", status, err)
	}
}
func TestScheduleReapplyPreservesNextRun(t *testing.T) {
	m, _ := setupManager(t)
	ctx := context.Background()
	id := "0123456789abcdef0123456789abcdef"
	s := Schedule{Frequency: "daily", Time: "09:00", KeepLast: 1}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := m.Configure(ctx, id, &s, now); err != nil {
		t.Fatal(err)
	}
	var before, after string
	_ = m.db.QueryRow(`SELECT next_run FROM backup_schedules`).Scan(&before)
	s.KeepLast = 3
	if err := m.Configure(ctx, id, &s, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = m.db.QueryRow(`SELECT next_run FROM backup_schedules`).Scan(&after)
	if before != after {
		t.Fatal("next run changed when only retention changed")
	}
}
