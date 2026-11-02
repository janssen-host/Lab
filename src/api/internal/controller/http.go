package controller

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/llllogan/lab-controller/internal/deploy"
)

//go:embed web/index.html
var webPage []byte

func routes(db *sql.DB, deployments *deploy.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(webPage)
	})
	mux.HandleFunc("POST /deploy", deployments.Deploy)
	mux.HandleFunc("GET /stacks", deployments.ListStacks)
	mux.HandleFunc("GET /stacks/{id}", deployments.StackDetails)
	mux.HandleFunc("GET /stacks/{id}/logs", deployments.StackLogs)
	mux.HandleFunc("POST /stacks/{id}/update", deployments.UpdateStack)
	mux.HandleFunc("POST /stacks/{id}/trust-host-key", deployments.TrustStackHostKey)
	mux.HandleFunc("POST /stacks/{id}/containers/{container}/restart", deployments.RestartContainer)
	mux.HandleFunc("GET /databases", deployments.ListDatabases)
	mux.HandleFunc("GET /databases/{id}", deployments.DatabaseDetails)
	mux.HandleFunc("POST /databases/{id}/backups", deployments.CreateBackup)
	mux.HandleFunc("POST /databases/{id}/backups/{backup}/restore", deployments.RestoreBackup)
	mux.HandleFunc("GET /jobs", deployments.Jobs)
	mux.HandleFunc("GET /jobs/{id}", deployments.Jobs)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), `SELECT n.name, n.type, n.hostname, n.ssh_port, n.postgres_port, n.reachable, n.checked_at, n.last_error, COALESCE(d.status, ''), COALESCE(d.last_error, ''), COALESCE(d.checked_at, '') FROM nodes n LEFT JOIN database_hosts d ON d.name=n.name WHERE n.configured=1 ORDER BY n.type, n.name`)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		defer rows.Close()
		nodes := []nodeStatus{}
		for rows.Next() {
			var n nodeStatus
			if err := rows.Scan(&n.Name, &n.Type, &n.Hostname, &n.SSHPort, &n.PostgresPort, &n.Reachable, &n.CheckedAt, &n.Error, &n.CommissioningStatus, &n.CommissioningError, &n.CommissioningCheckedAt); err != nil {
				http.Error(w, "database error", 500)
				return
			}
			nodes = append(nodes, n)
		}
		if rows.Err() != nil {
			http.Error(w, "database error", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodes)
	})
	return mux
}
