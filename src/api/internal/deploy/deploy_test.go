package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llllogan/lab-controller/internal/backup"
	"github.com/llllogan/lab-controller/internal/config"
	"github.com/llllogan/lab-controller/internal/dbcommission"
	_ "modernc.org/sqlite"
)

type remoteStub struct {
	queries        []string
	databaseExists bool
	roleExists     bool
	stackCalls     chan string
	stackErr       error
	lastDotenv     string
	restarted      string
	updated        string
	deleted        string
	deleteErr      error
}

func (r *remoteStub) DeployStack(_ context.Context, node config.Node, name string, compose, dotenv []byte) error {
	r.lastDotenv = string(dotenv)
	if r.stackCalls != nil {
		r.stackCalls <- node.Name + "/" + name
	}
	return r.stackErr
}
func (r *remoteStub) StackStatus(context.Context, config.Node, string) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"Name":"app-1","State":"running"}`)}, nil
}
func (r *remoteStub) StackServices(context.Context, config.Node, string) ([]string, error) {
	return nil, nil
}
func (r *remoteStub) RestartContainer(_ context.Context, node config.Node, stack, container string) error {
	r.restarted = node.Name + "/" + stack + "/" + container
	return nil
}
func (r *remoteStub) StreamLogs(context.Context, config.Node, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("first line\nsecond line\n")), nil
}
func (r *remoteStub) DeleteStack(_ context.Context, node config.Node, stack string) error {
	r.deleted = node.Name + "/" + stack
	return r.deleteErr
}

func (r *remoteStub) UpdateStack(_ context.Context, node config.Node, stack string) error {
	r.updated = node.Name + "/" + stack
	return nil
}
func (r *remoteStub) TrustHostKey(_ context.Context, node config.Node) (string, error) {
	r.updated = node.Name
	return "SHA256:test", nil
}

func TestTrustStackHostKey(t *testing.T) {
	s, remote := setup(t)
	if _, err := s.db.Exec(`INSERT INTO stacks VALUES ('s1','worker','app','now','now')`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/stacks/s1/trust-host-key", nil)
	req.SetPathValue("id", "s1")
	w := httptest.NewRecorder()
	s.TrustStackHostKey(w, req)
	if w.Code != http.StatusOK || remote.updated != "worker" || !strings.Contains(w.Body.String(), "SHA256:test") {
		t.Fatalf("unexpected response %d %s node %s", w.Code, w.Body.String(), remote.updated)
	}
	req.SetPathValue("id", "missing")
	w = httptest.NewRecorder()
	s.TrustStackHostKey(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing stack: %d", w.Code)
	}
}
func (r *remoteStub) Inspect(context.Context, config.Node) (bool, error) { return false, nil }
func (r *remoteStub) Dump(_ context.Context, _ config.Node, _, _ string, w io.Writer) error {
	_, err := w.Write([]byte("PGDMP-test"))
	return err
}
func (r *remoteStub) Start(context.Context, config.Node, string) error { return nil }
func (r *remoteStub) SQL(_ context.Context, _ config.Node, _ string, q string) (string, error) {
	r.queries = append(r.queries, q)
	if strings.Contains(q, "pg_database") && r.databaseExists {
		return "1", nil
	}
	if strings.Contains(q, "pg_roles") && r.roleExists {
		return "1", nil
	}
	return "", nil
}
func setup(t *testing.T) (*Service, *remoteStub) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE nodes (name TEXT PRIMARY KEY,type TEXT,hostname TEXT,ssh_port INTEGER,postgres_port INTEGER,configured INTEGER); INSERT INTO nodes VALUES('worker','workload','localhost',22,0,1),('db','database','localhost',22,5432,1)`)
	if err != nil {
		t.Fatal(err)
	}
	remote := &remoteStub{}
	key := strings.Repeat("aa", 32)
	commissioner, err := dbcommission.New(db, key, remote)
	if err != nil {
		t.Fatal(err)
	}
	if err = commissioner.Commission(context.Background(), config.Node{Name: "db"}); err != nil {
		t.Fatal(err)
	}
	service, err := New(db, key, commissioner, remote)
	if err != nil {
		t.Fatal(err)
	}
	return service, remote
}
func TestValidation(t *testing.T) {
	s, _ := setup(t)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"databases":[{"name":"app","user":"app_user","node":"db"}]}`, 201},
		{`{"name":"app","node":"db"}`, 400},
		{`{"databases":[{"name":"app","user":"app_user","node":"worker"}]}`, 400},
		{`{"databases":[{"name":"app;drop","user":"app_user","node":"db"}]}`, 400},
		{`{"databases":[{"name":"app","user":"app_user","node":"missing"}]}`, 400},
		{`{"name":"app","node":"worker"}`, 400},
		{`{"stacks":[{"name":"app","node":"worker"}]}`, 400},
		{`{`, 400},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/deploy", strings.NewReader(tc.body))
		s.Deploy(w, r)
		if w.Code != tc.status {
			t.Errorf("%s: got %d: %s", tc.body, w.Code, w.Body.String())
		}
	}
	// Wait for the asynchronous job before closing the test database.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		_ = s.db.QueryRow(`SELECT count(*) FROM jobs WHERE status IN ('pending','running')`).Scan(&count)
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
}
func TestCreatesDatabaseAndStoresEncryptedCredential(t *testing.T) {
	s, remote := setup(t)
	w := httptest.NewRecorder()
	s.Deploy(w, httptest.NewRequest("POST", "/deploy", strings.NewReader(`{"databases":[{"name":"app","user":"app_user","node":"db"}]}`)))
	var response struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Jobs) != 1 {
		t.Fatalf("response: %s, %v", w.Body.String(), err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		_ = s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, response.Jobs[0].ID).Scan(&status)
		if status == "succeeded" || status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "succeeded" {
		t.Fatalf("job status %s", status)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM databases WHERE name='app' AND username='app_user'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("database record: %d %v", count, err)
	}
	var ciphertext []byte
	if err := s.db.QueryRow(`SELECT encrypted_password FROM db_users WHERE username='app_user'`).Scan(&ciphertext); err != nil || len(ciphertext) == 0 {
		t.Fatalf("user credential missing: %v", err)
	}
	if len(remote.queries) != 4 {
		t.Fatalf("queries: %v", remote.queries)
	}
}

func TestExistingDatabaseIsLeftUntouched(t *testing.T) {
	s, remote := setup(t)
	remote.databaseExists = true
	if err := s.create(context.Background(), Database{Name: "app", User: "app_user", Node: "db"}); err != nil {
		t.Fatal(err)
	}
	if len(remote.queries) != 1 {
		t.Fatalf("expected only database lookup, got %v", remote.queries)
	}
}

func TestExistingUnmanagedRoleIsNotModified(t *testing.T) {
	s, remote := setup(t)
	remote.roleExists = true
	if err := s.create(context.Background(), Database{Name: "app", User: "app_user", Node: "db"}); err == nil {
		t.Fatal("expected unmanaged role error")
	}
	if len(remote.queries) != 2 {
		t.Fatalf("unexpected modifying query: %v", remote.queries)
	}
}

func TestBackupScheduleValidationAndPersistence(t *testing.T) {
	s, _ := setup(t)
	for _, body := range []string{
		`{"databases":[{"name":"app","user":"app_user","node":"db","backup_schedule":{"frequency":"monthly","keep_last":2}}]}`,
		`{"databases":[{"name":"app","user":"app_user","node":"db","backup_schedule":{"frequency":"daily","time":"99:00","keep_last":2}}]}`,
		`{"databases":[{"name":"app","user":"app_user","node":"db","backup_schedule":{"frequency":"hourly","keep_last":0}}]}`,
	} {
		w := httptest.NewRecorder()
		s.Deploy(w, httptest.NewRequest("POST", "/deploy", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid backup schedule accepted: %s (%d)", body, w.Code)
		}
	}
	body := `{"databases":[{"name":"app","user":"app_user","node":"db","backup_schedule":{"frequency":"daily","time":"02:00","keep_last":7}}]}`
	w := httptest.NewRecorder()
	s.Deploy(w, httptest.NewRequest("POST", "/deploy", strings.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Jobs) != 1 {
		t.Fatal("invalid response")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, response.Jobs[0].ID).Scan(&status)
		if status == "failed" {
			t.Fatal("database job failed")
		}
		if status == "succeeded" {
			var frequency, clock string
			var keep int
			err := s.db.QueryRow(`SELECT frequency,time,keep_last FROM backup_schedules`).Scan(&frequency, &clock, &keep)
			if err != nil || frequency != "daily" || clock != "02:00" || keep != 7 {
				t.Fatalf("schedule: %s %s %d %v", frequency, clock, keep, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job timed out")
}

func TestExistingManagedDatabaseCanUpdateOrDisableSchedule(t *testing.T) {
	s, remote := setup(t)
	ctx := context.Background()
	d := Database{Name: "app", User: "app_user", Node: "db"}
	if err := s.create(ctx, d); err != nil {
		t.Fatal(err)
	}
	remote.databaseExists = true
	d.Backup = &backup.Schedule{Frequency: "weekly", Time: "09:30", KeepLast: 3}
	if err := s.create(ctx, d); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM backup_schedules`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("schedule not created: %d %v", count, err)
	}
	d.Backup = nil
	if err := s.create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM backup_schedules`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("schedule not removed: %d %v", count, err)
	}
}

func TestRestartContainer(t *testing.T) {
	s, remote := setup(t)
	if _, err := s.db.Exec(`INSERT INTO stacks(id,node,name,created_at,updated_at) VALUES('stack1','worker','app','now','now')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stack, container string
		status           int
	}{
		{"stack1", "app-1", http.StatusNoContent},
		{"missing", "app-1", http.StatusNotFound},
		{"stack1", "bad;name", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.SetPathValue("id", tc.stack)
		req.SetPathValue("container", tc.container)
		w := httptest.NewRecorder()
		s.RestartContainer(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s/%s: got %d, want %d", tc.stack, tc.container, w.Code, tc.status)
		}
	}
	if remote.restarted != "worker/app/app-1" {
		t.Fatalf("wrong restart target: %q", remote.restarted)
	}
}

func TestUpdateStack(t *testing.T) {
	s, remote := setup(t)
	if _, err := s.db.Exec(`INSERT INTO stacks(id,node,name,created_at,updated_at) VALUES('stack1','worker','app','now','now')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		status int
	}{{"stack1", http.StatusNoContent}, {"missing", http.StatusNotFound}} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.SetPathValue("id", tc.id)
		w := httptest.NewRecorder()
		s.UpdateStack(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d, want %d", tc.id, w.Code, tc.status)
		}
	}
	if remote.updated != "worker/app" {
		t.Fatalf("wrong update target: %q", remote.updated)
	}
}

func TestDeleteStack(t *testing.T) {
	s, remote := setup(t)
	_, err := s.db.Exec(`INSERT INTO stacks VALUES ('s1','worker','app','now','now'); INSERT INTO stack_services VALUES ('s1','web',1); INSERT INTO stack_databases VALUES ('worker','app','db','data')`)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", "s1")
	remote.deleteErr = fmt.Errorf("host unavailable")
	w := httptest.NewRecorder()
	s.DeleteStack(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("failure: %d", w.Code)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM stacks WHERE id='s1'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("stack record lost on failure")
	}
	remote.deleteErr = nil
	w = httptest.NewRecorder()
	s.DeleteStack(w, req)
	if w.Code != http.StatusNoContent || remote.deleted != "worker/app" {
		t.Fatalf("delete: %d %s", w.Code, remote.deleted)
	}
	for _, table := range []string{"stacks", "stack_services", "stack_databases"} {
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s: %d %v", table, count, err)
		}
	}
}
