package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
)

type Restorer interface {
	Restore(context.Context, config.Node, string, string, string, io.Reader) error
}

var ErrBackupNotFound = errors.New("completed backup not found")
var safeIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func (m *Manager) StartRestore(databaseID, backupID string) (string, error) {
	var node, name, user, path string
	err := m.db.QueryRow(`SELECT d.node,d.name,d.username,b.path FROM databases d JOIN database_backups b ON b.database_id=d.id WHERE d.id=? AND b.id=? AND b.status='succeeded'`, databaseID, backupID).Scan(&node, &name, &user, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBackupNotFound
	}
	if err != nil {
		return "", err
	}
	if !safeIdentifier.MatchString(name) || !safeIdentifier.MatchString(user) || len(databaseID) != 32 || strings.Trim(databaseID, "0123456789abcdef") != "" || filepath.Dir(path) != databaseID || filepath.Base(path) == "." {
		return "", errors.New("invalid restore target or backup path")
	}
	if _, ok := m.dumper.(Restorer); !ok {
		return "", errors.New("restore is unavailable")
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = m.db.Exec(`INSERT INTO jobs(id,type,node,database_name,status,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, "restore", node, name, "running", "", stamp, stamp)
	if err != nil {
		return "", err
	}
	go func() {
		m.runMu.Lock()
		defer m.runMu.Unlock()
		err := m.restore(context.Background(), node, name, user, path)
		status, message := "succeeded", ""
		if err != nil {
			status, message = "failed", err.Error()
		}
		_, e := m.db.Exec(`UPDATE jobs SET status=?,last_error=?,updated_at=? WHERE id=?`, status, message, time.Now().UTC().Format(time.RFC3339Nano), id)
		if e != nil {
			fmt.Fprintf(os.Stderr, "record restore job: %v\n", e)
		}
	}()
	return id, nil
}
func (m *Manager) restore(ctx context.Context, node, name, user, path string) error {
	var n config.Node
	n.Name = node
	var status string
	if err := m.db.QueryRowContext(ctx, `SELECT n.hostname,n.ssh_port,COALESCE(h.status,'') FROM nodes n LEFT JOIN database_hosts h ON h.name=n.name WHERE n.name=? AND n.type='database' AND n.configured=1`, node).Scan(&n.Hostname, &n.SSHPort, &status); err != nil {
		return err
	}
	if status != "ready" {
		return errors.New("database host is not commissioned")
	}
	password, err := m.master.MasterPassword(ctx, node)
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(m.dir, path))
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 5)
	if _, err = io.ReadFull(f, magic); err != nil || string(magic) != "PGDMP" {
		return errors.New("invalid backup archive")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	return m.dumper.(Restorer).Restore(ctx, n, password, name, user, f)
}
