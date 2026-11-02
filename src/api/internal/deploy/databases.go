package deploy

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/llllogan/lab-controller/internal/backup"
)

type DatabaseRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Node      string `json:"node"`
	User      string `json:"user"`
	Hostname  string `json:"hostname"`
	Port      int    `json:"port"`
	CreatedAt string `json:"created_at"`
}
type DatabaseDetails struct {
	DatabaseRecord
	Password         string           `json:"password"`
	ConnectionString string           `json:"connection_string"`
	BackupSchedule   *backup.Schedule `json:"backup_schedule,omitempty"`
	Backups          []backup.Record  `json:"backups"`
}

const databasesQuery = `SELECT d.id,d.name,d.node,d.username,n.hostname,n.postgres_port,d.created_at FROM databases d JOIN nodes n ON n.name=d.node`

func (s *Service) ListDatabases(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), databasesQuery+` ORDER BY d.node,d.name`)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	records := []DatabaseRecord{}
	for rows.Next() {
		var d DatabaseRecord
		if err = rows.Scan(&d.ID, &d.Name, &d.Node, &d.User, &d.Hostname, &d.Port, &d.CreatedAt); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		records = append(records, d)
	}
	if rows.Err() != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(records)
}

func (s *Service) DatabaseDetails(w http.ResponseWriter, r *http.Request) {
	var d DatabaseRecord
	err := s.db.QueryRowContext(r.Context(), databasesQuery+` WHERE d.id=?`, r.PathValue("id")).Scan(&d.ID, &d.Name, &d.Node, &d.User, &d.Hostname, &d.Port, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "database not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	var encrypted []byte
	err = s.db.QueryRowContext(r.Context(), `SELECT encrypted_password FROM db_users WHERE node=? AND username=?`, d.Node, d.User).Scan(&encrypted)
	if err != nil {
		log.Printf("database %s credential unavailable: %v", d.ID, err)
		http.Error(w, "database credential unavailable", http.StatusInternalServerError)
		return
	}
	if len(encrypted) < s.aead.NonceSize() {
		http.Error(w, "database credential unavailable", http.StatusInternalServerError)
		return
	}
	password, err := s.aead.Open(nil, encrypted[:s.aead.NonceSize()], encrypted[s.aead.NonceSize():], []byte(d.Node+"/"+d.User))
	if err != nil {
		log.Printf("database %s credential could not be decrypted", d.ID)
		http.Error(w, "database credential unavailable", http.StatusInternalServerError)
		return
	}
	connection := url.URL{Scheme: "postgresql", User: url.UserPassword(d.User, string(password)), Host: net.JoinHostPort(d.Hostname, strconv.Itoa(d.Port)), Path: "/" + d.Name}
	backups, err := s.backups.List(r.Context(), d.ID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	schedule, err := s.backups.Schedule(r.Context(), d.ID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(DatabaseDetails{DatabaseRecord: d, Password: string(password), ConnectionString: connection.String(), BackupSchedule: schedule, Backups: backups})
}

func (s *Service) CreateBackup(w http.ResponseWriter, r *http.Request) {
	record, err := s.backups.StartNow(r.PathValue("id"))
	if errors.Is(err, backup.ErrDatabaseNotFound) {
		http.Error(w, "database not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "could not start backup", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(struct {
		Backup backup.Record `json:"backup"`
	}{record})
}

func (s *Service) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	id, err := s.backups.StartRestore(r.PathValue("id"), r.PathValue("backup"))
	if errors.Is(err, backup.ErrBackupNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "could not start restore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(struct {
		JobID string `json:"job_id"`
	}{id})
}
