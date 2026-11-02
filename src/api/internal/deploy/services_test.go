package deploy

import (
	"context"
	"encoding/json"
	"testing"
)

func TestServiceInventoryAndRunningCounts(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if err := s.replaceServices(ctx, "stack", []string{"web", "worker"}); err != nil {
		t.Fatal(err)
	}
	statuses := []json.RawMessage{json.RawMessage(`{"Service":"web","State":"running"}`), json.RawMessage(`{"Service":"web","State":"running"}`), json.RawMessage(`{"Service":"worker","State":"exited"}`)}
	if err := s.recordCounts(ctx, "stack", statuses); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"web": 2, "worker": 0} {
		var got int
		if err := s.db.QueryRow(`SELECT containers FROM stack_services WHERE stack_id='stack' AND name=?`, name).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count %d, error %v", name, got, err)
		}
	}
	if err := s.replaceServices(ctx, "stack", []string{"web"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM stack_services WHERE stack_id='stack'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("services %d: %v", count, err)
	}
}
