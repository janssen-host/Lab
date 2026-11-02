package dbcommission

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
	"golang.org/x/crypto/ssh"
)

// Dump streams a custom-format PostgreSQL dump over SSH without putting credentials in command arguments.
func (r SSHRemote) Dump(ctx context.Context, n config.Node, password, database string, dest io.Writer) error {
	key, err := os.ReadFile(r.KeyPath)
	if err != nil {
		return fmt.Errorf("read SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("parse SSH key: %w", err)
	}
	address := net.JoinHostPort(n.Hostname, strconv.Itoa(n.SSHPort))
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cc, chans, reqs, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "controller", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: r.verifyHostKey})
	if err != nil {
		return fmt.Errorf("SSH connection: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(password + "\n" + database + "\n")
	session.Stdout = dest
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-done:
		}
	}()
	const command = `set -eu
cd "$HOME/stacks/dbserver"
docker compose -p dbserver exec -T postgres sh -c 'IFS= read -r PGPASSWORD; IFS= read -r db; export PGPASSWORD; exec pg_dump -h 127.0.0.1 -U postgres -d "$db" --format=custom'`
	if err := session.Run(command); err != nil {
		return fmt.Errorf("pg_dump over SSH failed: %w", err)
	}
	return nil
}

// Restore replaces the database and streams the selected archive to pg_restore.
func (r SSHRemote) Restore(ctx context.Context, n config.Node, password, database, user string, source io.Reader) error {
	key, err := os.ReadFile(r.KeyPath)
	if err != nil {
		return fmt.Errorf("read SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("parse SSH key: %w", err)
	}
	address := net.JoinHostPort(n.Hostname, strconv.Itoa(n.SSHPort))
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cc, chans, reqs, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "controller", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: r.verifyHostKey})
	if err != nil {
		return fmt.Errorf("SSH connection: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = io.MultiReader(strings.NewReader(password+"\n"+database+"\n"+user+"\n"), source)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-done:
		}
	}()
	const command = `set -eu
cd "$HOME/stacks/dbserver"
docker compose -p dbserver exec -T postgres sh -c '
IFS= read -r PGPASSWORD; IFS= read -r db; IFS= read -r owner; export PGPASSWORD
archive=$(mktemp); trap "rm -f $archive" EXIT
cat > "$archive"
pg_restore --list "$archive" >/dev/null
psql -h 127.0.0.1 -U postgres -d postgres -X -v ON_ERROR_STOP=1 -c "DROP DATABASE \"$db\" WITH (FORCE)"
psql -h 127.0.0.1 -U postgres -d postgres -X -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$db\" OWNER \"$owner\""
pg_restore -h 127.0.0.1 -U postgres -d "$db" --no-owner --role="$owner" --exit-on-error "$archive"
'`
	if err := session.Run(command); err != nil {
		return fmt.Errorf("restore over SSH failed: %w", err)
	}
	return nil
}
