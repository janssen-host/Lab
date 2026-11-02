package controller

import (
	"context"
	"database/sql"
	"github.com/llllogan/lab-controller/internal/config"
	"log"
	"net"
	"strconv"
	"time"
)

func reconcile(ctx context.Context, db *sql.DB, c config.Config) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE nodes SET configured = 0`); err != nil {
		return err
	}
	for _, group := range []struct {
		kind  string
		nodes []config.Node
	}{{"workload", c.WorkloadHosts}, {"database", c.DatabaseHosts}} {
		for _, n := range group.nodes {
			conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(n.Hostname, strconv.Itoa(n.SSHPort)), 3*time.Second)
			message := ""
			if dialErr != nil {
				message = dialErr.Error()
			} else {
				conn.Close()
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO nodes (name, type, hostname, ssh_port, postgres_port, reachable, checked_at, last_error, configured)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
 ON CONFLICT(name) DO UPDATE SET type=excluded.type, hostname=excluded.hostname, ssh_port=excluded.ssh_port, postgres_port=excluded.postgres_port, reachable=excluded.reachable, checked_at=excluded.checked_at, last_error=excluded.last_error, configured=1`, n.Name, group.kind, n.Hostname, n.SSHPort, n.PostgresPort, dialErr == nil, time.Now().UTC().Format(time.RFC3339), message)
			if err != nil {
				return err
			}
			if dialErr != nil {
				log.Printf("node %s SSH port unreachable: %v", n.Name, dialErr)
			} else {
				log.Printf("node %s SSH port reachable", n.Name)
			}
		}
	}
	// Remove local metadata only. Workloads, databases and backup files on disk
	// are deliberately left intact when their host is detached.
	rows, err := tx.QueryContext(ctx, `SELECT hostname, ssh_port, configured FROM nodes`)
	if err != nil {
		return err
	}
	removed, retained := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var hostname string
		var port int
		var configured bool
		if err = rows.Scan(&hostname, &port, &configured); err != nil {
			rows.Close()
			return err
		}
		address := net.JoinHostPort(hostname, strconv.Itoa(port))
		if configured {
			retained[address] = true
		} else {
			removed[address] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for address := range removed {
		if !retained[address] {
			if _, err = tx.ExecContext(ctx, `DELETE FROM ssh_host_keys WHERE address=?`, address); err != nil {
				return err
			}
		}
	}
	for _, query := range []string{
		`DELETE FROM backup_schedules WHERE database_id IN (SELECT id FROM databases WHERE node IN (SELECT name FROM nodes WHERE configured=0))`,
		`DELETE FROM database_backups WHERE database_id IN (SELECT id FROM databases WHERE node IN (SELECT name FROM nodes WHERE configured=0))`,
		`DELETE FROM stack_databases WHERE stack_node IN (SELECT name FROM nodes WHERE configured=0) OR database_node IN (SELECT name FROM nodes WHERE configured=0)`,
		`DELETE FROM stack_services WHERE stack_id IN (SELECT id FROM stacks WHERE node IN (SELECT name FROM nodes WHERE configured=0))`,
		`DELETE FROM stacks WHERE node IN (SELECT name FROM nodes WHERE configured=0)`,
		`DELETE FROM databases WHERE node IN (SELECT name FROM nodes WHERE configured=0)`,
		`DELETE FROM db_users WHERE node IN (SELECT name FROM nodes WHERE configured=0)`,
		`DELETE FROM jobs WHERE node IN (SELECT name FROM nodes WHERE configured=0)`,
		`DELETE FROM database_hosts WHERE name IN (SELECT name FROM nodes WHERE configured=0)`,
		`DELETE FROM nodes WHERE configured=0`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}
