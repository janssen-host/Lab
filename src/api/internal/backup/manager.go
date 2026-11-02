package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
)

type Master interface {
	MasterPassword(context.Context, string) (string, error)
}
type Dumper interface {
	Dump(context.Context, config.Node, string, string, io.Writer) error
}
type Manager struct {
	db     *sql.DB
	dir    string
	master Master
	dumper Dumper
	runMu  sync.Mutex
}
type scheduled struct {
	dbID, node, name, frequency, clock, day, next string
	keep                                          int
}
type Record struct {
	ID          string `json:"id"`
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	Path        string `json:"path,omitempty"`
	Size        int64  `json:"size_bytes,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at,omitempty"`
}

func NewManager(db *sql.DB, dir string, master Master, dumper Dumper) (*Manager, error) {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS backup_schedules (database_id TEXT PRIMARY KEY, frequency TEXT NOT NULL, time TEXT NOT NULL, day TEXT NOT NULL, keep_last INTEGER NOT NULL, next_run TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS database_backups (id TEXT PRIMARY KEY, database_id TEXT NOT NULL, job_id TEXT NOT NULL, status TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, last_error TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	_, err := db.Exec(`UPDATE database_backups SET status='failed',last_error='API stopped before backup completed',completed_at=? WHERE status IN ('pending','running')`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return &Manager{db: db, dir: dir, master: master, dumper: dumper}, nil
}
func (m *Manager) Configure(ctx context.Context, dbID string, s *Schedule, now time.Time) error {
	if s == nil {
		_, err := m.db.ExecContext(ctx, `DELETE FROM backup_schedules WHERE database_id=?`, dbID)
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	day := s.Day
	if s.Frequency == "weekly" && day == "" {
		day = "monday"
	}
	clock := s.Time
	if s.Frequency == "hourly" {
		clock = ""
	}
	var frequency, oldTime, oldDay, next string
	var keep int
	err := m.db.QueryRowContext(ctx, `SELECT frequency,time,day,keep_last,next_run FROM backup_schedules WHERE database_id=?`, dbID).Scan(&frequency, &oldTime, &oldDay, &keep, &next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil || frequency != s.Frequency || oldTime != clock || oldDay != day {
		nextTime, e := s.Next(now)
		if e != nil {
			return e
		}
		next = nextTime.UTC().Format(time.RFC3339Nano)
	}
	_, err = m.db.ExecContext(ctx, `INSERT INTO backup_schedules(database_id,frequency,time,day,keep_last,next_run) VALUES(?,?,?,?,?,?) ON CONFLICT(database_id) DO UPDATE SET frequency=excluded.frequency,time=excluded.time,day=excluded.day,keep_last=excluded.keep_last,next_run=excluded.next_run`, dbID, s.Frequency, clock, day, s.KeepLast, next)
	return err
}

func (m *Manager) Schedule(ctx context.Context, dbID string) (*Schedule, error) {
	var schedule Schedule
	err := m.db.QueryRowContext(ctx, `SELECT frequency,time,day,keep_last FROM backup_schedules WHERE database_id=?`, dbID).Scan(&schedule.Frequency, &schedule.Time, &schedule.Day, &schedule.KeepLast)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if schedule.Frequency == "hourly" {
		schedule.Time = ""
	}
	if schedule.Frequency == "weekly" && schedule.Day == "monday" {
		schedule.Day = ""
	}
	return &schedule, nil
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := m.runDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Printf("backup scheduler: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) runDue(ctx context.Context, now time.Time) error {
	rows, err := m.db.QueryContext(ctx, `SELECT d.id,d.node,d.name,s.frequency,s.time,s.day,s.keep_last,s.next_run FROM backup_schedules s JOIN databases d ON d.id=s.database_id`)
	if err != nil {
		return err
	}
	var due []scheduled
	for rows.Next() {
		var x scheduled
		if err = rows.Scan(&x.dbID, &x.node, &x.name, &x.frequency, &x.clock, &x.day, &x.keep, &x.next); err != nil {
			rows.Close()
			return err
		}
		next, e := time.Parse(time.RFC3339Nano, x.next)
		if e != nil {
			rows.Close()
			return fmt.Errorf("invalid scheduled time for %s: %w", x.dbID, e)
		}
		if !next.After(now) {
			due = append(due, x)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = m.runOne(ctx, x, now); err != nil {
			log.Printf("backup for %s on %s failed: %v", x.name, x.node, err)
		}
	}
	return nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (m *Manager) runOne(ctx context.Context, x scheduled, now time.Time) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	schedule := Schedule{Frequency: x.frequency, Time: x.clock, Day: x.day, KeepLast: x.keep}
	next, err := schedule.Next(now)
	if err != nil {
		return err
	}
	jobID, err := randomID()
	if err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_schedules SET next_run=? WHERE database_id=? AND next_run=?`, next.Format(time.RFC3339Nano), x.dbID, x.next)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,type,node,database_name,status,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, jobID, "backup", x.node, x.name, "running", "", stamp, stamp); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO database_backups VALUES(?,?,?,?,?,?,?,?,?)`, id, x.dbID, jobID, "running", "", 0, "", stamp, ""); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	path, size, backupErr := m.create(ctx, x, id, now)
	status, message := "succeeded", ""
	if backupErr != nil {
		status, message = "failed", backupErr.Error()
	}
	completed := time.Now().UTC().Format(time.RFC3339Nano)
	// A canceled shutdown still needs to persist the failed result.
	recordCtx := context.Background()
	tx, err = m.db.BeginTx(recordCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(recordCtx, `UPDATE database_backups SET status=?,path=?,size=?,last_error=?,completed_at=? WHERE id=?`, status, path, size, message, completed, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(recordCtx, `UPDATE jobs SET status=?,last_error=?,updated_at=? WHERE id=?`, status, message, completed, jobID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if backupErr != nil {
		return backupErr
	}
	if err = m.prune(recordCtx, x.dbID); err != nil {
		log.Printf("backup retention for %s failed: %v", x.name, err)
	}
	return nil
}

var ErrDatabaseNotFound = errors.New("database not found")

func (m *Manager) StartNow(dbID string) (Record, error) {
	x := scheduled{dbID: dbID}
	if err := m.db.QueryRow(`SELECT node,name FROM databases WHERE id=?`, dbID).Scan(&x.node, &x.name); errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrDatabaseNotFound
	} else if err != nil {
		return Record{}, err
	}
	jobID, err := randomID()
	if err != nil {
		return Record{}, err
	}
	id, err := randomID()
	if err != nil {
		return Record{}, err
	}
	started := time.Now().UTC()
	stamp := started.Format(time.RFC3339Nano)
	tx, err := m.db.Begin()
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO jobs(id,type,node,database_name,status,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, jobID, "backup", x.node, x.name, "running", "", stamp, stamp); err != nil {
		return Record{}, err
	}
	if _, err = tx.Exec(`INSERT INTO database_backups VALUES(?,?,?,?,?,?,?,?,?)`, id, dbID, jobID, "running", "", 0, "", stamp, ""); err != nil {
		return Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	go m.completeNow(x, id, jobID, started)
	return Record{ID: id, JobID: jobID, Status: "running", StartedAt: stamp}, nil
}

func (m *Manager) completeNow(x scheduled, id, jobID string, started time.Time) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	path, size, backupErr := m.create(context.Background(), x, id, started)
	status, message := "succeeded", ""
	if backupErr != nil {
		status, message = "failed", backupErr.Error()
	}
	completed := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := m.db.Begin()
	if err == nil {
		if _, err = tx.Exec(`UPDATE database_backups SET status=?,path=?,size=?,last_error=?,completed_at=? WHERE id=?`, status, path, size, message, completed, id); err == nil {
			_, err = tx.Exec(`UPDATE jobs SET status=?,last_error=?,updated_at=? WHERE id=?`, status, message, completed, jobID)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	if err != nil {
		log.Printf("record ad hoc backup %s: %v", id, err)
		return
	}
	if backupErr != nil {
		log.Printf("ad hoc backup for %s on %s failed: %v", x.name, x.node, backupErr)
		return
	}
	if err = m.prune(context.Background(), x.dbID); err != nil {
		log.Printf("backup retention for %s failed: %v", x.name, err)
	}
}

func (m *Manager) create(ctx context.Context, x scheduled, id string, now time.Time) (string, int64, error) {
	var n config.Node
	n.Name = x.node
	var status string
	if err := m.db.QueryRowContext(ctx, `SELECT n.hostname,n.ssh_port,COALESCE(h.status,'') FROM nodes n LEFT JOIN database_hosts h ON h.name=n.name WHERE n.name=? AND n.type='database' AND n.configured=1`, x.node).Scan(&n.Hostname, &n.SSHPort, &status); err != nil {
		return "", 0, err
	}
	if status != "ready" {
		return "", 0, errors.New("database host is not commissioned")
	}
	password, err := m.master.MasterPassword(ctx, x.node)
	if err != nil {
		return "", 0, err
	}
	if len(x.dbID) != 32 || strings.Trim(x.dbID, "0123456789abcdef") != "" {
		return "", 0, errors.New("invalid database ID")
	}
	dir := filepath.Join(m.dir, x.dbID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", 0, fmt.Errorf("create backup directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return "", 0, err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	err = m.dumper.Dump(ctx, n, password, x.name, file)
	if err != nil {
		file.Close()
		return "", 0, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return "", 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return "", 0, err
	}
	if err = file.Close(); err != nil {
		return "", 0, err
	}
	if info.Size() < 5 {
		return "", 0, errors.New("backup is empty or incomplete")
	}
	f, err := os.Open(tmp)
	if err != nil {
		return "", 0, err
	}
	magic := make([]byte, 5)
	_, err = io.ReadFull(f, magic)
	f.Close()
	if err != nil || string(magic) != "PGDMP" {
		return "", 0, errors.New("backup is not a PostgreSQL custom-format dump")
	}
	name := now.UTC().Format("20060102T150405Z") + "-" + id + ".dump"
	relative := filepath.Join(x.dbID, name)
	if err = os.Rename(tmp, filepath.Join(m.dir, relative)); err != nil {
		return "", 0, err
	}
	return relative, info.Size(), nil
}
func (m *Manager) prune(ctx context.Context, dbID string) error {
	var keep int
	if err := m.db.QueryRowContext(ctx, `SELECT keep_last FROM backup_schedules WHERE database_id=?`, dbID).Scan(&keep); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id,path FROM database_backups WHERE database_id=? AND status='succeeded' ORDER BY started_at DESC,id DESC LIMIT -1 OFFSET ?`, dbID, keep)
	if err != nil {
		return err
	}
	type item struct{ id, path string }
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.path); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		if filepath.Dir(i.path) != dbID || filepath.Base(i.path) == "." || filepath.Ext(i.path) != ".dump" {
			return errors.New("invalid stored backup path")
		}
		if err = os.Remove(filepath.Join(m.dir, i.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err = m.db.ExecContext(ctx, `DELETE FROM database_backups WHERE id=?`, i.id); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) List(ctx context.Context, dbID string) ([]Record, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id,job_id,status,path,size,last_error,started_at,completed_at FROM database_backups WHERE database_id=? ORDER BY started_at DESC,id DESC`, dbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		var b Record
		if err = rows.Scan(&b.ID, &b.JobID, &b.Status, &b.Path, &b.Size, &b.Error, &b.StartedAt, &b.CompletedAt); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}
