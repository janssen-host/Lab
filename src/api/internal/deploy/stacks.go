package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"gopkg.in/yaml.v3"

	"github.com/llllogan/lab-controller/internal/config"
)

type stackRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Node      string `json:"node"`
	Hostname  string `json:"hostname"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Service) runStack(j Job, st Stack, files stackFiles, databases []Database) {
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='running',updated_at=? WHERE id=?`, timestamp(), j.ID); err != nil {
		return
	}
	err := s.deployStack(ctx, st, files, databases)
	status, message := "succeeded", ""
	if err != nil {
		status, message = "failed", err.Error()
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET status=?,last_error=?,updated_at=? WHERE id=?`, status, message, timestamp(), j.ID)
}
func (s *Service) deployStack(ctx context.Context, st Stack, files stackFiles, databases []Database) error {
	var compose struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(files.compose, &compose); err != nil {
		return fmt.Errorf("parse compose services: %w", err)
	}
	var n config.Node
	n.Name = st.Node
	if err := s.db.QueryRowContext(ctx, `SELECT hostname,ssh_port FROM nodes WHERE name=? AND type='workload' AND configured=1`, st.Node).Scan(&n.Hostname, &n.SSHPort); err != nil {
		return fmt.Errorf("workload host: %w", err)
	}
	if err := s.remote.DeployStack(ctx, n, st.Name, files.compose, files.dotenv); err != nil {
		return fmt.Errorf("deploy stack: %w", err)
	}
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	now := timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record stack after deployment: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO stacks(id,node,name,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(node,name) DO UPDATE SET updated_at=excluded.updated_at`, id, st.Node, st.Name, now, now); err != nil {
		return fmt.Errorf("record stack after deployment: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM stacks WHERE node=? AND name=?`, st.Node, st.Name).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM stack_services WHERE stack_id=?`, id); err != nil {
		return err
	}
	for name := range compose.Services {
		if _, err = tx.ExecContext(ctx, `INSERT INTO stack_services(stack_id,name) VALUES(?,?)`, id, name); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM stack_databases WHERE stack_node=? AND stack_name=?`, st.Node, st.Name); err != nil {
		return fmt.Errorf("record stack databases: %w", err)
	}
	for _, database := range databases {
		if _, err = tx.ExecContext(ctx, `INSERT INTO stack_databases(stack_node,stack_name,database_node,database_name) VALUES(?,?,?,?)`, st.Node, st.Name, database.Node, database.Name); err != nil {
			return fmt.Errorf("record stack database: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("record stack after deployment: %w", err)
	}
	return nil
}

const stackQuery = `SELECT s.id,s.name,s.node,n.hostname,s.created_at,s.updated_at FROM stacks s JOIN nodes n ON n.name=s.node`

func (s *Service) ListStacks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), stackQuery+` ORDER BY s.node,s.name`)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	records := []stackRecord{}
	for rows.Next() {
		var st stackRecord
		if err = rows.Scan(&st.ID, &st.Name, &st.Node, &st.Hostname, &st.CreatedAt, &st.UpdatedAt); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		records = append(records, st)
	}
	if rows.Err() != nil {
		http.Error(w, "database error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(records)
}
func (s *Service) StackDetails(w http.ResponseWriter, r *http.Request) {
	var st stackRecord
	var port int
	err := s.db.QueryRowContext(r.Context(), `SELECT s.id,s.name,s.node,n.hostname,s.created_at,s.updated_at,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE s.id=?`, r.PathValue("id")).Scan(&st.ID, &st.Name, &st.Node, &st.Hostname, &st.CreatedAt, &st.UpdatedAt, &port)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "stack not found", 404)
		return
	}
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	containers, err := s.remote.StackStatus(r.Context(), config.Node{Name: st.Node, Hostname: st.Hostname, SSHPort: port}, st.Name)
	if err != nil {
		http.Error(w, "could not retrieve stack status: "+err.Error(), http.StatusBadGateway)
		return
	}
	serviceRows, err := s.db.QueryContext(r.Context(), `SELECT name,containers FROM stack_services WHERE stack_id=? ORDER BY name`, st.ID)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	services := []serviceRecord{}
	for serviceRows.Next() {
		var v serviceRecord
		if err = serviceRows.Scan(&v.Name, &v.Containers); err != nil {
			break
		}
		services = append(services, v)
	}
	if err == nil {
		err = serviceRows.Err()
	}
	serviceRows.Close()
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	databaseRows, err := s.db.QueryContext(r.Context(), `SELECT d.id,d.name,d.node,d.username,n.hostname,n.postgres_port,d.created_at FROM stack_databases sd JOIN databases d ON d.node=sd.database_node AND d.name=sd.database_name JOIN nodes n ON n.name=d.node WHERE sd.stack_node=? AND sd.stack_name=? ORDER BY d.node,d.name`, st.Node, st.Name)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	databases := []DatabaseRecord{}
	for databaseRows.Next() {
		var d DatabaseRecord
		if err = databaseRows.Scan(&d.ID, &d.Name, &d.Node, &d.User, &d.Hostname, &d.Port, &d.CreatedAt); err != nil {
			databaseRows.Close()
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		databases = append(databases, d)
	}
	err = databaseRows.Err()
	databaseRows.Close()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		stackRecord
		Containers []json.RawMessage `json:"containers"`
		Services   []serviceRecord   `json:"services"`
		Databases  []DatabaseRecord  `json:"databases"`
	}{st, containers, services, databases})
}

// RestartContainer stops and starts the selected stack container.
func (s *Service) RestartContainer(w http.ResponseWriter, r *http.Request) {
	container := r.PathValue("container")
	if !containerName.MatchString(container) {
		http.Error(w, "invalid container name or ID", http.StatusBadRequest)
		return
	}
	var st stackRecord
	var port int
	err := s.db.QueryRowContext(r.Context(), `SELECT s.name,s.node,n.hostname,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE s.id=? AND n.type='workload' AND n.configured=1`, r.PathValue("id")).Scan(&st.Name, &st.Node, &st.Hostname, &port)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "stack not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	if err := s.remote.RestartContainer(r.Context(), config.Node{Name: st.Node, Hostname: st.Hostname, SSHPort: port}, st.Name, container); err != nil {
		http.Error(w, "could not restart container: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateStack pulls images and reconciles a recorded stack.
func (s *Service) TrustStackHostKey(w http.ResponseWriter, r *http.Request) {
	var node config.Node
	err := s.db.QueryRowContext(r.Context(), `SELECT n.name,n.hostname,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE s.id=? AND n.type='workload' AND n.configured=1`, r.PathValue("id")).Scan(&node.Name, &node.Hostname, &node.SSHPort)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "stack not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	fingerprint, err := s.remote.TrustHostKey(r.Context(), node)
	if err != nil {
		http.Error(w, "could not trust host key: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"node": node.Name, "fingerprint": fingerprint})
}

// UpdateStack pulls images and reconciles a recorded stack.
func (s *Service) UpdateStack(w http.ResponseWriter, r *http.Request) {
	var st stackRecord
	var port int
	err := s.db.QueryRowContext(r.Context(), `SELECT s.name,s.node,n.hostname,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE s.id=? AND n.type='workload' AND n.configured=1`, r.PathValue("id")).Scan(&st.Name, &st.Node, &st.Hostname, &port)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "stack not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	if err := s.remote.UpdateStack(r.Context(), config.Node{Name: st.Node, Hostname: st.Hostname, SSHPort: port}, st.Name); err != nil {
		http.Error(w, "could not update stack: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteStack removes the host project and its controller records.
func (s *Service) DeleteStack(w http.ResponseWriter, r *http.Request) {
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	var st stackRecord
	var port int
	err := s.db.QueryRowContext(r.Context(), `SELECT s.name,s.node,n.hostname,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE s.id=? AND n.type='workload' AND n.configured=1`, r.PathValue("id")).Scan(&st.Name, &st.Node, &st.Hostname, &port)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "stack not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if !stackName.MatchString(st.Name) {
		http.Error(w, "invalid stack name", http.StatusInternalServerError)
		return
	}
	if err = s.remote.DeleteStack(r.Context(), config.Node{Name: st.Node, Hostname: st.Hostname, SSHPort: port}, st.Name); err != nil {
		http.Error(w, "could not delete stack: "+err.Error(), http.StatusBadGateway)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer tx.Rollback()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM stack_services WHERE stack_id=?`, []any{r.PathValue("id")}},
		{`DELETE FROM stack_databases WHERE stack_node=? AND stack_name=?`, []any{st.Node, st.Name}},
		{`DELETE FROM stacks WHERE id=?`, []any{r.PathValue("id")}},
	} {
		if _, err = tx.ExecContext(r.Context(), q.sql, q.args...); err != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "database error", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
