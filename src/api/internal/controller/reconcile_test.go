package controller

import (
	"context"
	"database/sql"
	"github.com/llllogan/lab-controller/internal/config"
	"net"
	"path/filepath"
	"testing"
)

func TestReconcileRecordsReachabilityAndChanges(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	defer listener.Close()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE nodes (name TEXT PRIMARY KEY, type TEXT NOT NULL, hostname TEXT NOT NULL, ssh_port INTEGER NOT NULL, postgres_port INTEGER NOT NULL, reachable INTEGER NOT NULL, checked_at TEXT NOT NULL, last_error TEXT NOT NULL, configured INTEGER NOT NULL);
	CREATE TABLE database_hosts (name TEXT);
	CREATE TABLE stacks (node TEXT, name TEXT, id TEXT);
 CREATE TABLE stack_services (stack_id TEXT);
	CREATE TABLE databases (id TEXT, node TEXT);
	CREATE TABLE db_users (node TEXT);
	CREATE TABLE jobs (node TEXT);
	CREATE TABLE stack_databases (stack_node TEXT, database_node TEXT);
	CREATE TABLE backup_schedules (database_id TEXT);
	CREATE TABLE database_backups (database_id TEXT);
	CREATE TABLE ssh_host_keys (address TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{WorkloadHosts: []config.Node{{Name: "worker", Hostname: "127.0.0.1", SSHPort: port}}}
	if err := reconcile(context.Background(), db, c); err != nil {
		t.Fatal(err)
	}
	var reachable, configured bool
	var checked, message string
	if err := db.QueryRow(`SELECT reachable, configured, checked_at, last_error FROM nodes WHERE name='worker'`).Scan(&reachable, &configured, &checked, &message); err != nil {
		t.Fatal(err)
	}
	if !reachable || !configured || checked == "" || message != "" {
		t.Fatalf("unexpected status: %v %v %q %q", reachable, configured, checked, message)
	}
	listener.Close()
	if err := reconcile(context.Background(), db, c); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT reachable, last_error FROM nodes WHERE name='worker'`).Scan(&reachable, &message); err != nil {
		t.Fatal(err)
	}
	if reachable || message == "" {
		t.Fatalf("expected failed connection, got %v %q", reachable, message)
	}
	_, err = db.Exec(`INSERT INTO nodes VALUES ('keep','workload','127.0.0.1',1,0,0,'','',1);
	INSERT INTO stacks VALUES ('worker','removed','1'), ('keep','retained','2');
	INSERT INTO databases VALUES ('removed-db','worker'), ('retained-db','keep');
	INSERT INTO database_hosts VALUES ('worker'), ('keep');
	INSERT INTO db_users VALUES ('worker'), ('keep');
	INSERT INTO jobs VALUES ('worker'), ('keep');
	INSERT INTO backup_schedules VALUES ('removed-db'), ('retained-db');
	INSERT INTO database_backups VALUES ('removed-db'), ('retained-db');
	INSERT INTO stack_databases VALUES ('worker','keep'), ('keep','worker'), ('keep','keep');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcile(context.Background(), db, config.Config{WorkloadHosts: []config.Node{{Name: "keep", Hostname: "127.0.0.1", SSHPort: port}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT configured FROM nodes WHERE name='worker'`).Scan(&configured); err != sql.ErrNoRows {
		t.Fatalf("removed node still exists: %v", err)
	}
	for _, table := range []string{"nodes", "database_hosts", "stacks", "databases", "db_users", "jobs", "backup_schedules", "database_backups", "stack_databases"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: got %d records, error %v; want one retained record", table, count, err)
		}
	}
}
