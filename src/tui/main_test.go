package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
)

func TestLoadsStacksContainersAndLogs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[{"name":"worker","type":"workload"}]`) })
	mux.HandleFunc("GET /stacks", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"s1","name":"web","node":"worker"}]`)
	})
	mux.HandleFunc("GET /stacks/s1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"containers":[{"ID":"abc123","Name":"web-1","Service":"web","State":"running"}]}`)
	})
	mux.HandleFunc("GET /stacks/s1/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("container") != "abc123" {
			t.Errorf("unexpected container %q", r.URL.Query().Get("container"))
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("hello log\n"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	api, err := newAPI(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := &model{api: api, width: 100, height: 20}
	_, cmd := m.Update(m.Init()())
	if cmd == nil {
		t.Fatal("did not request containers")
	}
	_, cmd = m.Update(cmd())
	if cmd == nil {
		t.Fatal("did not open logs")
	}
	_, cmd = m.Update(cmd())
	if cmd == nil {
		t.Fatal("did not read logs")
	}
	_, _ = m.Update(cmd())
	defer m.stopStream()
	if len(m.stacks) != 1 || len(m.containers) != 1 || len(m.logs) != 1 || m.logs[0] != "hello log" {
		t.Fatalf("unexpected model: %+v", m)
	}
	if view := m.View(); !strings.Contains(view, "hello log") || !strings.Contains(view, "[web]") {
		t.Fatalf("unexpected view: %s", view)
	}
}
func TestNavigationAndStaleResponses(t *testing.T) {
	m := &model{stacks: []stack{{ID: "a"}, {ID: "b"}}, containers: []container{{ID: "one"}, {ID: "two"}}, width: 80, height: 20}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.containerIndex != 1 || cmd == nil {
		t.Fatal("right arrow did not select container")
	}
	generation := m.generation
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.stackIndex != 1 || cmd == nil || m.generation == generation {
		t.Fatal("down arrow did not select stack")
	}
	m.Update(containersMsg{generation: generation, containers: []container{{Name: "stale"}}})
	if len(m.containers) != 0 {
		t.Fatal("stale response changed selection")
	}
	m.appendLogs(strings.Repeat("one\n", 30))
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.scroll == 0 {
		t.Fatal("page up did not scroll")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.scroll != 0 {
		t.Fatal("page down did not return to live output")
	}
	m.stopStream()
}
func TestAPIValidation(t *testing.T) {
	for _, v := range []string{"", "ws://localhost", "http://user:pass@host", "http://host?x=1"} {
		if _, err := newAPI(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}

func TestViewHasTitledBoxesWithinTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{{25, 7}, {80, 24}, {120, 40}} {
		m := &model{width: size.width, height: size.height, stacks: []stack{{Name: "example"}}, containers: []container{{Service: "web"}}, logs: []string{"hello"}}
		view := m.View()
		if !strings.Contains(view, "┌─ Stacks ") || !strings.Contains(view, "┌─ Logs ") || !strings.Contains(view, "└") {
			t.Fatalf("missing box borders: %s", view)
		}
		rows := strings.Split(view, "\n")
		if len(rows) > size.height {
			t.Fatalf("height %d exceeds %d", len(rows), size.height)
		}
		for _, line := range rows {
			if width := lipgloss.Width(line); width > size.width {
				t.Fatalf("width %d exceeds %d: %q", width, size.width, line)
			}
		}
	}
}

func TestStackNodeDetails(t *testing.T) {
	m := &model{width: 120, height: 24, stacks: []stack{{Name: "app", Node: "worker"}}, containers: []container{{Name: "app-1", Service: "web", State: "running"}}, nodes: []node{{Name: "worker", Type: "workload", Hostname: "10.10.20.2", SSHPort: 22, Reachable: true}}}
	view := m.View()
	for _, value := range []string{"10.10.20.2", "workload", "Yes"} {
		if !strings.Contains(view, value) {
			t.Fatalf("missing %q in view: %s", value, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("line exceeds terminal width: %q", line)
		}
	}
}

func TestHorizontalLogScroll(t *testing.T) {
	m := &model{width: 50, height: 12, stacks: []stack{{Name: "stack"}}, containers: []container{{Service: "app"}}, logs: []string{strings.Repeat("0123456789", 10)}}
	before := m.View()
	m.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonWheelRight})
	if m.horizontal != 4 {
		t.Fatalf("horizontal offset %d", m.horizontal)
	}
	after := m.View()
	if before == after || !strings.Contains(after, "4567890123") {
		t.Fatalf("horizontal scroll did not change visible log: %s", after)
	}
	m.Update(tea.MouseMsg{X: 1, Y: 5, Button: tea.MouseButtonWheelRight})
	if m.horizontal != 4 {
		t.Fatal("scroll outside log pane changed offset")
	}
	m.Update(tea.MouseMsg{X: 20, Y: 5, Button: tea.MouseButtonWheelLeft})
	if m.horizontal != 0 {
		t.Fatal("horizontal scroll did not return to start")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.horizontal != 4 {
		t.Fatal("keyboard fallback did not scroll")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.horizontal != 0 {
		t.Fatal("keyboard fallback did not return")
	}
}

func TestSelectionModeShowsOnlyFullWidthLogs(t *testing.T) {
	m := &model{width: 30, height: 3, selectText: true, logs: []string{"first", "second", "third", "fourth"}, status: "status"}
	if got := m.View(); got != "second\nthird\nfourth" {
		t.Fatalf("unexpected selection view: %q", got)
	}
	m.scroll = 1
	if got := m.View(); got != "first\nsecond\nthird" {
		t.Fatalf("unexpected scrolled view: %q", got)
	}
}

func TestScrollStopsAtOldestVisibleLine(t *testing.T) {
	m := &model{width: 80, height: 12, stacks: []stack{{Name: "stack"}}, containers: []container{{Name: "container"}}, logs: []string{"one", "two", "three", "four", "five", "six"}}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.scroll != len(m.logs)-m.logHeight() {
		t.Fatalf("scroll=%d", m.scroll)
	}
	if !strings.Contains(m.View(), "one") {
		t.Fatal("oldest line is not visible")
	}
}

func TestStacksGroupedByNode(t *testing.T) {
	m := &model{width: 100, height: 20}
	m.Update(stacksMsg{stacks: []stack{{Name: "two", Node: "b"}, {Name: "one", Node: "a"}, {Name: "three", Node: "b"}}})
	if m.stacks[0].Name != "one" || m.stacks[1].Name != "two" || m.stacks[2].Name != "three" {
		t.Fatalf("unexpected ordering: %#v", m.stacks)
	}
	view := m.View()
	if !strings.Contains(view, "a ") || !strings.Contains(view, "b ") {
		t.Fatalf("missing node headers: %s", view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.stackIndex != 1 || m.stacks[m.stackIndex].Name != "two" {
		t.Fatal("navigation failed across node header")
	}
}

func TestEmptyWorkloadNodesShown(t *testing.T) {
	m := &model{width: 100, height: 20, nodes: []node{{Name: "empty", Type: "workload"}, {Name: "db", Type: "database"}}}
	view := m.View()
	if !strings.Contains(view, "empty") || strings.Contains(view, "db ") {
		t.Fatalf("unexpected nodes: %s", view)
	}
}

func TestLogColourRules(t *testing.T) {
	rules, err := parseLogColours(defaultLogColours)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		line   string
		colour string
	}{
		{"2026-10-28T11:32:29Z ERR failure", "1"},
		{"2026-10-28T11:32:29Z WRN warning", "3"},
		{`{"level" : "warn"}`, "3"},
		{`{"level":"ERROR"}`, "1"},
		{`{"level":"error"}`, "1"},
		{"2026-10-28T11:32:29Z INF info", ""},
		{"2026-10-29 20:48:01,924::WARNING::message", "3"},
		{"2026-10-29 20:48:01,924::ERROR::message", "1"},
		{"2026-10-29 20:48:01,924::CRITICAL::message", "1"},
		{"2026-10-29 20:48:01,924::INFO::message", ""},
		{"[11:59:59.243] [INF] Server started", ""},
		{"[11:59:59.243] [WRN] Warning", "3"},
		{"[11:59:59.243] [ERR] Failure", "1"},
		{"[11:59:59.243] [FTL] Fatal", "1"},
		{"[11:59:59.243] [DBG] Debug", "4"},
		{"[11:59:59.243] [VRB] Verbose", "4"},
		{"[Info] Server started", ""},
		{"[Debug] Request sent", "4"},
		{"[Trace] Request sent", "4"},
		{"[Warn] Problem", "3"},
		{"[Error] Failed", "1"},
		{"[Fatal] Failed", "1"},
	} {
		got := ""
		for _, rule := range rules {
			if rule.pattern.MatchString(tc.line) {
				got = string(rule.colour)
				break
			}
		}
		if got != tc.colour {
			t.Errorf("%q: got %q, want %q", tc.line, got, tc.colour)
		}
	}
	for _, bad := range []string{`{"colours":{},"patterns":[{"colour":"missing","regex":"x"}]}`, `{"colours":{"x":"1"},"patterns":[{"colour":"x","regex":"["}]}`} {
		if _, err := parseLogColours([]byte(bad)); err == nil {
			t.Errorf("accepted invalid config %s", bad)
		}
	}
}

func TestUpdateStackKey(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/stacks/s1/update" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	api, err := newAPI(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := &model{api: api, stacks: []stack{{ID: "s1", Name: "app"}}}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	if cmd == nil || !m.updating {
		t.Fatal("update not started")
	}
	msg := cmd()
	if !called {
		t.Fatal("update endpoint not called")
	}
	updated, ok := msg.(stackUpdatedMsg)
	if !ok || updated.err != nil {
		t.Fatalf("update failed: %#v", msg)
	}
}

func TestDeleteStackConfirmations(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/stacks/s1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	api, err := newAPI(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := &model{api: api, stacks: []stack{{ID: "s1", Name: "app"}}}
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	_, cmd := m.Update(key("D"))
	if cmd != nil || m.deleteConfirm != 1 {
		t.Fatal("first confirmation missing")
	}
	_, cmd = m.Update(key("y"))
	if cmd != nil || m.deleteConfirm != 2 || called {
		t.Fatal("second confirmation missing")
	}
	m.Update(key("n"))
	if m.deleteConfirm != 0 || called {
		t.Fatal("cancel failed")
	}
	m.Update(key("D"))
	m.Update(key("y"))
	_, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("delete not started")
	}
	msg := cmd()
	if result, ok := msg.(stackDeletedMsg); !ok || result.err != nil || !called {
		t.Fatalf("delete failed: %#v", msg)
	}
}
