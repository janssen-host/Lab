package deploy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestStackLogsWebSocket(t *testing.T) {
	s, _ := setup(t)
	if _, err := s.db.Exec(`INSERT INTO stacks(id,node,name,created_at,updated_at) VALUES('stack1','worker','app','now','now')`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stacks/{id}/logs", s.StackLogs)
	server := httptest.NewServer(mux)
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/stacks/stack1/logs?container=app-1"
	ws, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	var received strings.Builder
	for received.Len() < len("first line\nsecond line\n") {
		typ, data, err := ws.ReadMessage()
		if err != nil || typ != websocket.TextMessage {
			t.Fatalf("log message: %q %v", data, err)
		}
		received.Write(data)
	}
	if got := received.String(); got != "first line\nsecond line\n" {
		t.Fatalf("unexpected logs: %q", got)
	}
	_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/stacks/missing/logs?container=app-1", nil)
	if err == nil || response == nil || response.StatusCode != 404 {
		t.Fatalf("missing stack: %v %v", response, err)
	}
	response.Body.Close()
	_, response, err = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/stacks/stack1/logs?container=../bad", nil)
	if err == nil || response == nil || response.StatusCode != 400 {
		t.Fatalf("invalid container: %v %v", response, err)
	}
	response.Body.Close()
}
