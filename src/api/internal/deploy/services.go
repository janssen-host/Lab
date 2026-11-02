package deploy

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
)

type serviceRecord struct {
	Name       string `json:"name"`
	Containers int    `json:"containers"`
}
type monitoredStack struct {
	id, name string
	node     config.Node
}

// MonitorServices discovers services at startup and refreshes running counts every five minutes.
func (s *Service) MonitorServices(ctx context.Context) {
	s.refreshServices(ctx, true)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshServices(ctx, false)
		}
	}
}
func (s *Service) refreshServices(ctx context.Context, discover bool) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.name,n.name,n.hostname,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE n.type='workload' AND n.configured=1`)
	if err != nil {
		log.Printf("service monitoring: %v", err)
		return
	}
	var stacks []monitoredStack
	for rows.Next() {
		var st monitoredStack
		if err = rows.Scan(&st.id, &st.name, &st.node.Name, &st.node.Hostname, &st.node.SSHPort); err != nil {
			break
		}
		stacks = append(stacks, st)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		log.Printf("service monitoring: %v", err)
		return
	}
	for _, st := range stacks {
		if ctx.Err() != nil {
			return
		}
		s.stackMu.Lock()
		if discover {
			names, e := s.remote.StackServices(ctx, st.node, st.name)
			if e == nil {
				e = s.replaceServices(ctx, st.id, names)
			}
			if e != nil {
				log.Printf("discover services for %s/%s: %v", st.node.Name, st.name, e)
			}
		}
		statuses, e := s.remote.StackStatus(ctx, st.node, st.name)
		if e == nil {
			e = s.recordCounts(ctx, st.id, statuses)
		}
		if e != nil {
			log.Printf("count containers for %s/%s: %v", st.node.Name, st.name, e)
		}
		s.stackMu.Unlock()
	}
}
func (s *Service) replaceServices(ctx context.Context, id string, names []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Keep existing counts until the subsequent status check succeeds.
	for _, name := range names {
		if _, err = tx.ExecContext(ctx, `INSERT INTO stack_services(stack_id,name) VALUES(?,?) ON CONFLICT DO NOTHING`, id, name); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM stack_services WHERE stack_id=?`, id)
	if err != nil {
		return err
	}
	var old []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		old = append(old, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, name := range names {
		present[name] = true
	}
	for _, name := range old {
		if !present[name] {
			if _, err = tx.ExecContext(ctx, `DELETE FROM stack_services WHERE stack_id=? AND name=?`, id, name); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (s *Service) recordCounts(ctx context.Context, id string, statuses []json.RawMessage) error {
	counts := map[string]int{}
	for _, raw := range statuses {
		var item struct {
			Service string
			State   string
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if item.State == "running" {
			counts[item.Service]++
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE stack_services SET containers=0 WHERE stack_id=?`, id); err != nil {
		return err
	}
	for name, count := range counts {
		if _, err = tx.ExecContext(ctx, `UPDATE stack_services SET containers=? WHERE stack_id=? AND name=?`, count, id, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
