package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stackRequest(t *testing.T, forearm string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{"forearm": forearm, "compose": "services:\n  web:\n    image: nginx:alpine\n", "env": `{"GREETING":"hello"}`} {
		part, err := writer.CreateFormFile(name, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/deploy", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestStackJobCreatesDirectory(t *testing.T) {
	for _, body := range []string{`{"name":"my-app","node":"worker"}`} {
		t.Run(body, func(t *testing.T) {
			s, remote := setup(t)
			remote.stackCalls = make(chan string, 1)
			w := httptest.NewRecorder()
			s.Deploy(w, stackRequest(t, body))
			var result struct {
				Jobs []Job `json:"jobs"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 201 || len(result.Jobs) != 1 || result.Jobs[0].Stack != "my-app" {
				t.Fatalf("response: %d %s", w.Code, w.Body.String())
			}
			select {
			case call := <-remote.stackCalls:
				if call != "worker/my-app" {
					t.Fatalf("wrong target: %s", call)
				}
			case <-time.After(time.Second):
				t.Fatal("stack job did not run")
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				var status string
				_ = s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, result.Jobs[0].ID).Scan(&status)
				if status == "succeeded" {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("stack job did not succeed")
		})
	}
}

func TestStackNameRejectsPathTraversal(t *testing.T) {
	s, _ := setup(t)
	for _, name := range []string{"../other", "/tmp/test", ".", "a/b", "a\ncommand"} {
		data, _ := json.Marshal(Forearm{Name: name, Node: "worker"})
		w := httptest.NewRecorder()
		s.Deploy(w, stackRequest(t, string(data)))
		if w.Code != 400 {
			t.Fatalf("name %q: HTTP %d", name, w.Code)
		}
	}
}

func TestStackJobRecordsFailure(t *testing.T) {
	s, remote := setup(t)
	remote.stackErr = errors.New("SSH unavailable")
	w := httptest.NewRecorder()
	s.Deploy(w, stackRequest(t, `{"name":"app","node":"worker"}`))
	var result struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Jobs) != 1 {
		t.Fatal("invalid response")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var status, message string
		err := s.db.QueryRow(`SELECT status,last_error FROM jobs WHERE id=?`, result.Jobs[0].ID).Scan(&status, &message)
		if err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			if !strings.Contains(message, "SSH unavailable") {
				t.Fatal(message)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stack job did not fail")
}

func TestStackRecordsAndLiveDetails(t *testing.T) {
	s, remote := setup(t)
	w := httptest.NewRecorder()
	s.Deploy(w, stackRequest(t, `{"name":"my-app","node":"worker"}`))
	var result struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Jobs) != 1 {
		t.Fatalf("invalid response %s", w.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, result.Jobs[0].ID).Scan(&status)
		if status == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	list := httptest.NewRecorder()
	s.ListStacks(list, httptest.NewRequest("GET", "/stacks", nil))
	var records []stackRecord
	if err := json.Unmarshal(list.Body.Bytes(), &records); err != nil || len(records) != 1 {
		t.Fatalf("list: %s %v", list.Body.String(), err)
	}
	if records[0].Name != "my-app" || remote.lastDotenv != "GREETING='hello'\n" {
		t.Fatal("wrong stack or files")
	}
	req := httptest.NewRequest("GET", "/stacks/"+records[0].ID, nil)
	req.SetPathValue("id", records[0].ID)
	detail := httptest.NewRecorder()
	s.StackDetails(detail, req)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"State":"running"`) {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
	missing := httptest.NewRequest("GET", "/stacks/missing", nil)
	missing.SetPathValue("id", "missing")
	detail = httptest.NewRecorder()
	s.StackDetails(detail, missing)
	if detail.Code != 404 {
		t.Fatalf("missing status %d", detail.Code)
	}
}
