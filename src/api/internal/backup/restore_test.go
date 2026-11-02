package backup

import (
	"context"
	"github.com/llllogan/lab-controller/internal/config"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type restoreStub struct {
	dumpStub
	called chan string
}

func (r *restoreStub) Restore(_ context.Context, _ config.Node, _, name, user string, src io.Reader) error {
	b, err := io.ReadAll(src)
	if err == nil {
		r.called <- name + "/" + user + "/" + string(b)
	}
	return err
}
func TestRestoreUsesCompletedBackupForDatabase(t *testing.T) {
	m, _ := setupManager(t)
	id := "0123456789abcdef0123456789abcdef"
	if _, err := m.db.Exec(`ALTER TABLE databases ADD COLUMN username TEXT NOT NULL DEFAULT 'appuser'`); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.dir, id), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(id, "backup.dump")
	if err := os.WriteFile(filepath.Join(m.dir, path), []byte("PGDMP-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`INSERT INTO database_backups VALUES('backup',?,'job','succeeded',?,13,'','now','now')`, id, path); err != nil {
		t.Fatal(err)
	}
	stub := &restoreStub{called: make(chan string, 1)}
	m.dumper = stub
	if _, err := m.StartRestore("other", "backup"); err != ErrBackupNotFound {
		t.Fatalf("unexpected error %v", err)
	}
	job, err := m.StartRestore(id, "backup")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stub.called:
		if got != "app/appuser/PGDMP-content" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restore not invoked")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := m.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, job).Scan(&status); err == nil && status == "succeeded" {
			return
		}
		time.Sleep(time.Millisecond * 10)
	}
	t.Fatal("job did not complete")
}
