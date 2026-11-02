package dbcommission

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llllogan/lab-controller/internal/config"
	_ "modernc.org/sqlite"
)

type fakeRemote struct {
	existing  bool
	startErr  error
	inspected int
	passwords []string
}

func (f *fakeRemote) Inspect(context.Context, config.Node) (bool, error) {
	f.inspected++
	return f.existing, nil
}
func (f *fakeRemote) Start(_ context.Context, _ config.Node, password string) error {
	f.passwords = append(f.passwords, password)
	return f.startErr
}
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func TestCommissionKeepsEncryptedPasswordAcrossFailureAndRestart(t *testing.T) {
	db := newTestDB(t)
	r := &fakeRemote{startErr: errors.New("not ready")}
	key := strings.Repeat("ab", 32)
	c, err := New(db, key, r)
	if err != nil {
		t.Fatal(err)
	}
	node := config.Node{Name: "db1", PostgresPort: 5432}
	if err = c.Commission(context.Background(), node); err == nil {
		t.Fatal("expected error")
	}
	var encrypted []byte
	var status string
	if err = db.QueryRow("SELECT encrypted_password, status FROM database_hosts WHERE name='db1'").Scan(&encrypted, &status); err != nil {
		t.Fatal(err)
	}
	if status != "error" || strings.Contains(string(encrypted), r.passwords[0]) {
		t.Fatal("password not encrypted or failure not recorded")
	}
	r.startErr = nil
	c, err = New(db, key, r)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Commission(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if len(r.passwords) != 2 || r.passwords[0] != r.passwords[1] || r.inspected != 1 {
		t.Fatal("password changed or host reinspected")
	}
	if err = db.QueryRow("SELECT status FROM database_hosts WHERE name='db1'").Scan(&status); err != nil || status != "ready" {
		t.Fatalf("status %q: %v", status, err)
	}
	wrong, err := New(db, strings.Repeat("cd", 32), r)
	if err != nil {
		t.Fatal(err)
	}
	if err = wrong.Commission(context.Background(), node); err == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestExistingHostWithoutCredentialIsNotStarted(t *testing.T) {
	db := newTestDB(t)
	r := &fakeRemote{existing: true}
	c, err := New(db, strings.Repeat("ab", 32), r)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Commission(context.Background(), config.Node{Name: "db1"}); err == nil {
		t.Fatal("existing host accepted")
	}
	if len(r.passwords) != 0 {
		t.Fatal("existing host started")
	}
}
func TestInvalidEncryptionKey(t *testing.T) {
	if _, err := New(newTestDB(t), "invalid", &fakeRemote{}); err == nil {
		t.Fatal("invalid key accepted")
	}
}
