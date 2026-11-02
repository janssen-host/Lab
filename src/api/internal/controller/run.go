package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/llllogan/lab-controller/internal/config"
	"github.com/llllogan/lab-controller/internal/dbcommission"
	"github.com/llllogan/lab-controller/internal/deploy"
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func Run() error {
	configPath := envOr("NODES_CONFIG", "/app/config/nodes.yaml")
	dbPath := envOr("DATABASE_PATH", "/app/data/controller.sqlite")
	c, err := config.Read(configPath)
	if err != nil {
		return fmt.Errorf("read nodes configuration: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS nodes (name TEXT PRIMARY KEY, type TEXT NOT NULL, hostname TEXT NOT NULL, ssh_port INTEGER NOT NULL, postgres_port INTEGER NOT NULL, reachable INTEGER NOT NULL, checked_at TEXT NOT NULL, last_error TEXT NOT NULL, configured INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	commissioner, err := dbcommission.New(db, os.Getenv("DB_ENCRYPTION_KEY"), dbcommission.SSHRemote{
		KeyPath: "/app/ssh/id_ed25519", DB: db,
	})
	if err != nil {
		return fmt.Errorf("initialize database commissioning: %w", err)
	}
	deployService, err := deploy.New(db, os.Getenv("DB_ENCRYPTION_KEY"), commissioner, dbcommission.SSHRemote{KeyPath: "/app/ssh/id_ed25519", DB: db})
	if err != nil {
		return fmt.Errorf("initialize deployments: %w", err)
	}
	if err = reconcile(context.Background(), db, c); err != nil {
		return fmt.Errorf("reconcile nodes: %w", err)
	}
	for _, host := range c.DatabaseHosts {
		if err := commissioner.Commission(context.Background(), host); err != nil {
			log.Printf("database host %s commissioning failed: %v", host.Name, err)
		}
	}
	server := &http.Server{Addr: ":8080", Handler: routes(db, deployService), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	servicesDone := make(chan struct{})
	go func() { defer close(servicesDone); deployService.MonitorServices(ctx) }()
	backupsDone := make(chan struct{})
	go func() { defer close(backupsDone); deployService.RunBackups(ctx) }()
	defer func() { stop(); <-backupsDone; <-servicesDone }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("API listening on %s", server.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
