package dbcommission

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
)

type Remote interface {
	Inspect(context.Context, config.Node) (bool, error)
	Start(context.Context, config.Node, string) error
}

type Commissioner struct {
	db     *sql.DB
	aead   cipher.AEAD
	remote Remote
}

func New(db *sql.DB, key string, remote Remote) (*Commissioner, error) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("DB_ENCRYPTION_KEY must be 32 bytes encoded as 64 hexadecimal characters")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS database_hosts (name TEXT PRIMARY KEY REFERENCES nodes(name), encrypted_password BLOB, status TEXT NOT NULL, last_error TEXT NOT NULL, checked_at TEXT NOT NULL)`)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ssh_host_keys (address TEXT PRIMARY KEY, public_key BLOB NOT NULL)`)
	if err != nil {
		return nil, err
	}
	return &Commissioner{db: db, aead: aead, remote: remote}, nil
}

func (c *Commissioner) Commission(ctx context.Context, n config.Node) error {
	err := c.commission(ctx, n)
	status, message := "ready", ""
	if err != nil {
		status, message = "error", err.Error()
	}
	_, recordErr := c.db.ExecContext(ctx, `INSERT INTO database_hosts(name,status,last_error,checked_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET status=excluded.status,last_error=excluded.last_error,checked_at=excluded.checked_at`, n.Name, status, message, time.Now().UTC().Format(time.RFC3339))
	if recordErr != nil {
		return fmt.Errorf("record commissioning result: %w", recordErr)
	}
	return err
}

func (c *Commissioner) MasterPassword(ctx context.Context, name string) (string, error) {
	var encrypted []byte
	var status string
	if err := c.db.QueryRowContext(ctx, `SELECT encrypted_password, status FROM database_hosts WHERE name=?`, name).Scan(&encrypted, &status); err != nil {
		return "", err
	}
	if status != "ready" {
		return "", errors.New("database host is not commissioned")
	}
	if len(encrypted) < c.aead.NonceSize() {
		return "", errors.New("stored credential is invalid")
	}
	plain, err := c.aead.Open(nil, encrypted[:c.aead.NonceSize()], encrypted[c.aead.NonceSize():], []byte(name))
	if err != nil {
		return "", errors.New("cannot decrypt stored master credential")
	}
	return string(plain), nil
}

func (c *Commissioner) commission(ctx context.Context, n config.Node) error {
	var encrypted []byte
	err := c.db.QueryRowContext(ctx, `SELECT encrypted_password FROM database_hosts WHERE name=?`, n.Name).Scan(&encrypted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var password string
	if len(encrypted) == 0 {
		exists, err := c.remote.Inspect(ctx, n)
		if err != nil {
			return fmt.Errorf("inspect host: %w", err)
		}
		if exists {
			return errors.New("existing database volume or environment file found without a stored credential")
		}
		raw := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return err
		}
		password = hex.EncodeToString(raw)
		nonce := make([]byte, c.aead.NonceSize())
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return err
		}
		encrypted = c.aead.Seal(nonce, nonce, []byte(password), []byte(n.Name))
		_, err = c.db.ExecContext(ctx, `INSERT INTO database_hosts(name,encrypted_password,status,last_error,checked_at) VALUES(?,?,'initializing','',?) ON CONFLICT(name) DO UPDATE SET encrypted_password=excluded.encrypted_password,status=excluded.status,last_error='',checked_at=excluded.checked_at`, n.Name, encrypted, time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return fmt.Errorf("store credential: %w", err)
		}
	} else {
		if len(encrypted) < c.aead.NonceSize() {
			return errors.New("stored credential is invalid")
		}
		nonce := encrypted[:c.aead.NonceSize()]
		raw, err := c.aead.Open(nil, nonce, encrypted[c.aead.NonceSize():], []byte(n.Name))
		if err != nil {
			return errors.New("cannot decrypt stored credential; check DB_ENCRYPTION_KEY")
		}
		password = string(raw)
	}
	if err := c.remote.Start(ctx, n, password); err != nil {
		return fmt.Errorf("start or verify PostgreSQL: %w", err)
	}
	return nil
}
