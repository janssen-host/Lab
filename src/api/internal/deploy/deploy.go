package deploy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/llllogan/lab-controller/internal/backup"
	"github.com/llllogan/lab-controller/internal/config"
	"github.com/llllogan/lab-controller/internal/dbcommission"
)

type Stack struct {
	Name        string `json:"name"`
	Node        string `json:"node"`
	ComposeFile string `json:"compose_file,omitempty"`
}
type Database struct {
	Name   string           `json:"name"`
	User   string           `json:"user"`
	Node   string           `json:"node"`
	Backup *backup.Schedule `json:"backup_schedule,omitempty"`
}
type Forearm struct {
	Name      string     `json:"name,omitempty"`
	Node      string     `json:"node,omitempty"`
	Databases []Database `json:"databases,omitempty"`
}
type Service struct {
	db           *sql.DB
	aead         cipher.AEAD
	commissioner *dbcommission.Commissioner
	remote       SQLRemote
	mu           sync.Mutex
	stackMu      sync.Mutex
	backups      *backup.Manager
}
type SQLRemote interface {
	SQL(context.Context, config.Node, string, string) (string, error)
	DeployStack(context.Context, config.Node, string, []byte, []byte) error
	StackStatus(context.Context, config.Node, string) ([]json.RawMessage, error)
	StackServices(context.Context, config.Node, string) ([]string, error)
	RestartContainer(context.Context, config.Node, string, string) error
	UpdateStack(context.Context, config.Node, string) error
	TrustHostKey(context.Context, config.Node) (string, error)
	StreamLogs(context.Context, config.Node, string, string) (io.ReadCloser, error)
	Dump(context.Context, config.Node, string, string, io.Writer) error
}
type Job struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Node      string `json:"node"`
	Database  string `json:"database,omitempty"`
	Stack     string `json:"stack,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
var stackName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func New(db *sql.DB, key string, commissioner *dbcommission.Commissioner, remote SQLRemote) (*Service, error) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid encryption key")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, type TEXT NOT NULL, node TEXT NOT NULL, database_name TEXT NOT NULL, status TEXT NOT NULL, last_error TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS stacks (id TEXT PRIMARY KEY, node TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(node,name))`,
		`CREATE TABLE IF NOT EXISTS stack_services (stack_id TEXT NOT NULL, name TEXT NOT NULL, containers INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(stack_id,name))`,
		`CREATE TABLE IF NOT EXISTS stack_databases (stack_node TEXT NOT NULL, stack_name TEXT NOT NULL, database_node TEXT NOT NULL, database_name TEXT NOT NULL, PRIMARY KEY(stack_node,stack_name,database_node,database_name))`,
		`CREATE TABLE IF NOT EXISTS db_users (node TEXT NOT NULL, username TEXT NOT NULL, encrypted_password BLOB NOT NULL, PRIMARY KEY(node,username))`,
		`CREATE TABLE IF NOT EXISTS databases (id TEXT PRIMARY KEY, node TEXT NOT NULL, name TEXT NOT NULL, username TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(node,name))`,
	} {
		if _, err = db.Exec(query); err != nil {
			return nil, err
		}
	}
	// Jobs are not recoverable; make interruption visible rather than leave them running forever.
	if _, err = db.Exec(`UPDATE jobs SET status='failed', last_error='API stopped before job completed', updated_at=? WHERE status IN ('pending','running')`, timestamp()); err != nil {
		return nil, err
	}
	dir := os.Getenv("BACKUP_DIR")
	if dir == "" {
		dir = "/app/backups"
	}
	manager, err := backup.NewManager(db, dir, commissioner, remote)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, aead: aead, commissioner: commissioner, remote: remote, backups: manager}, nil
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (s *Service) validate(ctx context.Context, f Forearm) error {
	if (f.Name == "") != (f.Node == "") {
		return errors.New("stack requires both name and node")
	}
	if f.Name != "" {
		if !stackName.MatchString(f.Name) || strings.TrimSpace(f.Node) == "" {
			return errors.New("stack requires a node and a name of 1-128 letters, digits, dots, underscores or hyphens starting with a letter or digit")
		}
		if err := s.nodeType(ctx, f.Node, "workload"); err != nil {
			return fmt.Errorf("stack: %w", err)
		}
	}
	seen := map[string]bool{}
	for i, d := range f.Databases {
		if !identifier.MatchString(d.Name) || !identifier.MatchString(d.User) {
			return fmt.Errorf("databases[%d] name and user must be lowercase PostgreSQL identifiers (1-63 characters)", i)
		}
		if err := s.nodeType(ctx, d.Node, "database"); err != nil {
			return fmt.Errorf("databases[%d]: %w", i, err)
		}
		if d.Backup != nil {
			if err := d.Backup.Validate(); err != nil {
				return fmt.Errorf("databases[%d] backup_schedule: %w", i, err)
			}
		}
		k := d.Node + "/" + d.Name
		if seen[k] {
			return fmt.Errorf("databases[%d]: duplicate database on node", i)
		}
		seen[k] = true
	}
	if f.Name == "" && len(f.Databases) == 0 {
		return errors.New("at least one stack or database is required")
	}
	return nil
}
func (s *Service) nodeType(ctx context.Context, name, kind string) error {
	var actual string
	err := s.db.QueryRowContext(ctx, `SELECT type FROM nodes WHERE name=? AND configured=1`, name).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("unknown node %q", name)
	}
	if err != nil {
		return err
	}
	if actual != kind {
		return fmt.Errorf("node %q is %s, expected %s", name, actual, kind)
	}
	return nil
}
func (s *Service) Deploy(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	f, files, err := parseRequest(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.validate(r.Context(), f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "could not create jobs", 500)
		return
	}
	defer tx.Rollback()
	jobs := make([]Job, 0, len(f.Databases)+1)
	if f.Name != "" {
		id, e := randomHex(16)
		if e != nil {
			http.Error(w, "could not create jobs", 500)
			return
		}
		now := timestamp()
		j := Job{ID: id, Type: "stack", Node: f.Node, Stack: f.Name, Status: "pending", CreatedAt: now, UpdatedAt: now}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO jobs VALUES(?,?,?,?,?,?,?,?)`, j.ID, j.Type, j.Node, j.Stack, j.Status, "", now, now); e != nil {
			http.Error(w, "could not create jobs", 500)
			return
		}
		jobs = append(jobs, j)
	}
	for _, d := range f.Databases {
		id, e := randomHex(16)
		if e != nil {
			http.Error(w, "could not create jobs", 500)
			return
		}
		now := timestamp()
		j := Job{ID: id, Type: "database", Node: d.Node, Database: d.Name, Status: "pending", CreatedAt: now, UpdatedAt: now}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO jobs VALUES(?,?,?,?,?,?,?,?)`, j.ID, j.Type, j.Node, j.Database, j.Status, "", now, now); e != nil {
			http.Error(w, "could not create jobs", 500)
			return
		}
		jobs = append(jobs, j)
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "could not create jobs", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Jobs []Job `json:"jobs"`
	}{jobs})
	stackJobs := 0
	if f.Name != "" {
		go s.runStack(jobs[0], Stack{Name: f.Name, Node: f.Node}, files, f.Databases)
		stackJobs = 1
	}
	for i, d := range f.Databases {
		go s.run(jobs[stackJobs+i], d)
	}
}
func (s *Service) run(j Job, d Database) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='running',updated_at=? WHERE id=?`, timestamp(), j.ID); err != nil {
		return
	}
	err := s.create(ctx, d)
	status, msg := "succeeded", ""
	if err != nil {
		status, msg = "failed", err.Error()
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET status=?,last_error=?,updated_at=? WHERE id=?`, status, msg, timestamp(), j.ID)
}
func (s *Service) create(ctx context.Context, d Database) error {
	var n config.Node
	n.Name = d.Node
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT n.hostname,n.ssh_port,n.postgres_port, COALESCE(h.status,'') FROM nodes n LEFT JOIN database_hosts h ON n.name=h.name WHERE n.name=? AND n.type='database' AND n.configured=1`, d.Node).Scan(&n.Hostname, &n.SSHPort, &n.PostgresPort, &status); err != nil {
		return fmt.Errorf("database host: %w", err)
	}
	if status != "ready" {
		return errors.New("database host is not commissioned")
	}
	master, err := s.commissioner.MasterPassword(ctx, d.Node)
	if err != nil {
		return err
	}
	query := func(q string) (string, error) { return s.remote.SQL(ctx, n, master, q) }
	exists, err := query(fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s';", d.Name))
	if err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if exists == "1" {
		var id string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM databases WHERE node=? AND name=?`, d.Node, d.Name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if d.Backup != nil {
				return errors.New("cannot schedule backups for an existing database without an API record")
			}
			return nil
		}
		if err != nil {
			return err
		}
		return s.backups.Configure(ctx, id, d.Backup, time.Now())
	}
	var ciphertext []byte
	err = s.db.QueryRowContext(ctx, `SELECT encrypted_password FROM db_users WHERE node=? AND username=?`, d.Node, d.User).Scan(&ciphertext)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	role, err := query(fmt.Sprintf("SELECT 1 FROM pg_roles WHERE rolname = '%s';", d.User))
	if err != nil {
		return fmt.Errorf("check database user: %w", err)
	}
	if role == "1" && len(ciphertext) == 0 {
		return errors.New("database user already exists without a stored credential")
	}
	if len(ciphertext) == 0 {
		pass, err := randomHex(32)
		if err != nil {
			return err
		}
		nonce := make([]byte, s.aead.NonceSize())
		if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
			return err
		}
		ciphertext = s.aead.Seal(nonce, nonce, []byte(pass), []byte(d.Node+"/"+d.User))
		if _, err = s.db.ExecContext(ctx, `INSERT INTO db_users(node,username,encrypted_password) VALUES(?,?,?)`, d.Node, d.User, ciphertext); err != nil {
			return err
		}
	}
	if role != "1" {
		if len(ciphertext) < s.aead.NonceSize() {
			return errors.New("invalid database user credential")
		}
		pass, err := s.aead.Open(nil, ciphertext[:s.aead.NonceSize()], ciphertext[s.aead.NonceSize():], []byte(d.Node+"/"+d.User))
		if err != nil {
			return errors.New("cannot decrypt database user credential")
		}
		_, err = query(fmt.Sprintf(`CREATE ROLE "%s" LOGIN PASSWORD '%s';`, d.User, string(pass)))
		if err != nil {
			return fmt.Errorf("create database user: %w", err)
		}
	}
	_, err = query(fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s";`, d.Name, d.User))
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO databases(id,node,name,username,created_at) VALUES(?,?,?,?,?)`, id, d.Node, d.Name, d.User, timestamp())
	if err != nil {
		return err
	}
	return s.backups.Configure(ctx, id, d.Backup, time.Now())
}

func (s *Service) RunBackups(ctx context.Context) { s.backups.Run(ctx) }
func (s *Service) Jobs(w http.ResponseWriter, r *http.Request) {
	query := `SELECT id,type,node,database_name,status,last_error,created_at,updated_at FROM jobs`
	args := []any{}
	if id := r.PathValue("id"); id != "" {
		query += ` WHERE id=?`
		args = append(args, id)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var j Job
		var target string
		if err = rows.Scan(&j.ID, &j.Type, &j.Node, &target, &j.Status, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		if j.Type == "stack" {
			j.Stack = target
		} else {
			j.Database = target
		}
		jobs = append(jobs, j)
	}
	if rows.Err() != nil {
		http.Error(w, "database error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.PathValue("id") != "" {
		if len(jobs) == 0 {
			http.Error(w, "job not found", 404)
			return
		}
		_ = json.NewEncoder(w).Encode(jobs[0])
		return
	}
	_ = json.NewEncoder(w).Encode(jobs)
}
