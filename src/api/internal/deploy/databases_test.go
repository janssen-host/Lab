package deploy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDatabaseEndpoints(t *testing.T) {
	s, _ := setup(t)
	if _, err := s.db.Exec(`UPDATE nodes SET hostname='2001:db8::1',postgres_port=5433 WHERE name='db'`); err != nil {
		t.Fatal(err)
	}
	if err := s.create(context.Background(), Database{Name: "app", User: "app_user", Node: "db"}); err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	s.ListDatabases(list, httptest.NewRequest("GET", "/databases", nil))
	var records []DatabaseRecord
	if err := json.Unmarshal(list.Body.Bytes(), &records); err != nil || len(records) != 1 {
		t.Fatalf("list: %d %s %v", list.Code, list.Body.String(), err)
	}
	if strings.Contains(list.Body.String(), "password") {
		t.Fatal("database list contains password")
	}
	req := httptest.NewRequest("GET", "/databases/"+records[0].ID, nil)
	req.SetPathValue("id", records[0].ID)
	detail := httptest.NewRecorder()
	s.DatabaseDetails(detail, req)
	var d DatabaseDetails
	if err := json.Unmarshal(detail.Body.Bytes(), &d); err != nil || detail.Code != 200 {
		t.Fatalf("detail: %d %s %v", detail.Code, detail.Body.String(), err)
	}
	u, err := url.Parse(d.ConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := u.User.Password()
	if d.ID != records[0].ID || len(d.Password) != 64 || pass != d.Password || u.User.Username() != "app_user" || u.Host != "[2001:db8::1]:5433" || u.Path != "/app" {
		t.Fatalf("incorrect database details: %+v", d.DatabaseRecord)
	}
	if detail.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	if d.Backups == nil || len(d.Backups) != 0 {
		t.Fatal("expected an empty backups array")
	}
	if _, err = s.db.Exec(`INSERT INTO database_backups VALUES ('backup1', ?, 'job1', 'succeeded', 'dir/file.dump', 123, '', 'start', 'end')`, d.ID); err != nil {
		t.Fatal(err)
	}
	detail = httptest.NewRecorder()
	s.DatabaseDetails(detail, req)
	if err = json.Unmarshal(detail.Body.Bytes(), &d); err != nil || len(d.Backups) != 1 || d.Backups[0].ID != "backup1" || d.Backups[0].Size != 123 {
		t.Fatalf("backup details: %+v, %v", d.Backups, err)
	}
	missing := httptest.NewRequest("GET", "/databases/missing", nil)
	missing.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	s.DatabaseDetails(w, missing)
	if w.Code != 404 {
		t.Fatalf("missing record: %d", w.Code)
	}
	if _, err = s.db.Exec(`UPDATE db_users SET encrypted_password=x'00' WHERE username='app_user'`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.DatabaseDetails(w, req)
	if w.Code != 500 {
		t.Fatalf("bad credential: %d", w.Code)
	}
}
