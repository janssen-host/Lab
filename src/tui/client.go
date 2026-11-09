package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type node struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Hostname  string `json:"hostname"`
	SSHPort   int    `json:"ssh_port"`
	Reachable bool   `json:"reachable"`
	CheckedAt string `json:"checked_at"`
}
type stack struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Node string `json:"node"`
}
type container struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
}
type databaseRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	User     string `json:"user"`
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
}
type databaseDetail struct {
	databaseRecord
	Password         string          `json:"password"`
	ConnectionString string          `json:"connection_string"`
	BackupSchedule   *scheduleDetail `json:"backup_schedule"`
	Backups          []backupRecord  `json:"backups"`
}
type scheduleDetail struct {
	Frequency string `json:"frequency"`
	Time      string `json:"time"`
	Day       string `json:"day"`
	KeepLast  int    `json:"keep_last"`
}
type backupRecord struct {
	ID          string `json:"id"`
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	Path        string `json:"path"`
	Size        int64  `json:"size_bytes"`
	Error       string `json:"error"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}
type stackDetails struct {
	Containers []container `json:"containers"`
	Services   []struct {
		Name       string `json:"name"`
		Containers int    `json:"containers"`
	} `json:"services"`
	Databases []databaseRecord `json:"databases"`
}
type apiClient struct {
	base *url.URL
	http *http.Client
}

func newAPI(raw string) (*apiClient, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("API URL must be an http or https URL without credentials, query, or fragment")
	}
	return &apiClient{base: u, http: &http.Client{Timeout: 15 * time.Second}}, nil
}
func (c *apiClient) endpoint(path string) *url.URL {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	return &u
}
func (c *apiClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path).String(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("invalid API response: %w", err)
	}
	return nil
}
func (c *apiClient) nodes(ctx context.Context) ([]node, error) {
	var result []node
	err := c.get(ctx, "/nodes", &result)
	return result, err
}
func (c *apiClient) stacks(ctx context.Context) ([]stack, error) {
	var result []stack
	err := c.get(ctx, "/stacks", &result)
	return result, err
}
func (c *apiClient) containers(ctx context.Context, id string) ([]container, error) {
	var result stackDetails
	err := c.get(ctx, "/stacks/"+url.PathEscape(id), &result)
	return result.Containers, err
}
func (c *apiClient) stackDetails(ctx context.Context, id string) (stackDetails, error) {
	var result stackDetails
	err := c.get(ctx, "/stacks/"+url.PathEscape(id), &result)
	return result, err
}
func (c *apiClient) databaseDetails(ctx context.Context, id string) (databaseDetail, error) {
	var result databaseDetail
	err := c.get(ctx, "/databases/"+url.PathEscape(id), &result)
	return result, err
}
func (c *apiClient) createBackup(ctx context.Context, id string) (backupRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/databases/"+url.PathEscape(id)+"/backups").String(), nil)
	if err != nil {
		return backupRecord{}, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return backupRecord{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		return backupRecord{}, fmt.Errorf("API returned %s", res.Status)
	}
	var result struct {
		Backup backupRecord `json:"backup"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return backupRecord{}, fmt.Errorf("invalid API response: %w", err)
	}
	return result.Backup, nil
}
func (c *apiClient) logs(ctx context.Context, id, name string) (*websocket.Conn, error) {
	u := c.endpoint("/stacks/" + url.PathEscape(id) + "/logs")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	q := u.Query()
	q.Set("container", name)
	u.RawQuery = q.Encode()
	conn, res, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if res != nil {
			res.Body.Close()
			return nil, fmt.Errorf("log stream: %s", res.Status)
		}
		return nil, fmt.Errorf("log stream: %w", err)
	}
	return conn, nil
}

func (c *apiClient) restartContainer(ctx context.Context, stackID, containerName string) error {
	u := c.endpoint("/stacks/" + url.PathEscape(stackID) + "/containers/" + url.PathEscape(containerName) + "/restart")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}
	// A graceful stop can take longer than the client's usual 15-second timeout.
	client := *c.http
	client.Timeout = 180 * time.Second
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("API returned %s", res.Status)
	}
	return nil
}

func (c *apiClient) updateStack(ctx context.Context, stackID string) error {
	u := c.endpoint("/stacks/" + url.PathEscape(stackID) + "/update")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}
	client := *c.http
	client.Timeout = 180 * time.Second
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("API returned %s", res.Status)
	}
	return nil
}

func (c *apiClient) deleteStack(ctx context.Context, stackID string) error {
	u := c.endpoint("/stacks/" + url.PathEscape(stackID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	client := *c.http
	client.Timeout = 180 * time.Second
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("API returned %s", res.Status)
	}
	return nil
}

func (c *apiClient) trustStackHostKey(ctx context.Context, stackID string) (string, error) {
	u := c.endpoint("/stacks/" + url.PathEscape(stackID) + "/trust-host-key")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return "", err
	}
	client := *c.http
	client.Timeout = 25 * time.Second
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned %s", res.Status)
	}
	var result struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Fingerprint, nil
}

func (c *apiClient) restoreBackup(ctx context.Context, databaseID, backupID string) (string, error) {
	u := c.endpoint("/databases/" + url.PathEscape(databaseID) + "/backups/" + url.PathEscape(backupID) + "/restore")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("API returned %s", res.Status)
	}
	var result struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.JobID, nil
}
