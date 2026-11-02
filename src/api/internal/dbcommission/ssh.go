package dbcommission

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
	"golang.org/x/crypto/ssh"
)

type SSHRemote struct {
	KeyPath string
	DB      *sql.DB
}

// TrustHostKey explicitly replaces a node's stored key after a successful SSH
// handshake and authentication. Failed connections leave the old key intact.
func (r SSHRemote) TrustHostKey(ctx context.Context, n config.Node) (string, error) {
	key, err := os.ReadFile(r.KeyPath)
	if err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", err
	}
	address := net.JoinHostPort(n.Hostname, strconv.Itoa(n.SSHPort))
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	var hostKey ssh.PublicKey
	client, chans, reqs, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User: "controller", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { hostKey = key; return nil },
	})
	if err != nil {
		return "", err
	}
	sshClient := ssh.NewClient(client, chans, reqs)
	defer sshClient.Close()
	if hostKey == nil {
		return "", fmt.Errorf("server supplied no host key")
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO ssh_host_keys(address,public_key) VALUES(?,?) ON CONFLICT(address) DO UPDATE SET public_key=excluded.public_key`, address, hostKey.Marshal())
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(hostKey), nil
}

func (r SSHRemote) verifyHostKey(hostname string, _ net.Addr, key ssh.PublicKey) error {
	result, err := r.DB.Exec(`INSERT INTO ssh_host_keys(address, public_key) VALUES(?, ?) ON CONFLICT(address) DO NOTHING`, hostname, key.Marshal())
	if err != nil {
		return fmt.Errorf("store SSH host key: %w", err)
	}
	var saved []byte
	if err := r.DB.QueryRow(`SELECT public_key FROM ssh_host_keys WHERE address = ?`, hostname).Scan(&saved); err != nil {
		return fmt.Errorf("read SSH host key: %w", err)
	}
	if !bytes.Equal(saved, key.Marshal()) {
		return fmt.Errorf("SSH host key changed for %s", hostname)
	}
	if inserted, err := result.RowsAffected(); err == nil && inserted == 1 {
		log.Printf("trusted first SSH host key for %s: %s", hostname, ssh.FingerprintSHA256(key))
	}
	return nil
}

func (r SSHRemote) run(ctx context.Context, n config.Node, script, input string) (string, error) {
	key, err := os.ReadFile(r.KeyPath)
	if err != nil {
		return "", fmt.Errorf("read SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("parse SSH key: %w", err)
	}
	address := net.JoinHostPort(n.Hostname, strconv.Itoa(n.SSHPort))
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(180 * time.Second)
	if v, ok := ctx.Deadline(); ok && v.Before(deadline) {
		deadline = v
	}
	_ = conn.SetDeadline(deadline)
	cc, chans, reqs, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "controller", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: r.verifyHostKey, Timeout: 5 * time.Second})
	if err != nil {
		return "", fmt.Errorf("SSH connection: %w", err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(input)
	var out bytes.Buffer
	session.Stdout = &out
	if err := session.Run(script); err != nil {
		if exit, ok := err.(*ssh.ExitError); ok {
			switch exit.ExitStatus() {
			case 40:
				return "", fmt.Errorf("stack directory or existing configuration is invalid")
			case 41:
				return "", fmt.Errorf("Compose deployment failed; previous stack configuration was restored")
			case 42:
				return "", fmt.Errorf("Compose deployment failed and restoration also failed")
			case 31:
				return "", fmt.Errorf("existing database password differs from stored credential")
			case 32:
				return "", fmt.Errorf("Docker Compose failed to start PostgreSQL")
			case 33:
				return "", fmt.Errorf("PostgreSQL port mapping does not match nodes.yaml; update the host Compose file")
			case 34:
				return "", fmt.Errorf("PostgreSQL did not become ready or authentication failed")
			}
		}
		return "", fmt.Errorf("remote command failed: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

func (r SSHRemote) Inspect(ctx context.Context, n config.Node) (bool, error) {
	const script = `set -eu; cd "$HOME/stacks/dbserver"; test -f docker-compose.yml; docker info >/dev/null 2>&1; if test -e .env || docker volume ls -q --filter name=^dbserver_postgres_data$ | grep -Fxq dbserver_postgres_data; then echo existing; else echo empty; fi`
	out, err := r.run(ctx, n, script, "")
	if err != nil {
		return false, err
	}
	if out != "existing" && out != "empty" {
		return false, fmt.Errorf("unexpected host inspection result")
	}
	return out == "existing", nil
}

func (r SSHRemote) Start(ctx context.Context, n config.Node, password string) error {
	const script = `set -eu
cd "$HOME/stacks/dbserver"
IFS= read -r password
IFS= read -r port
umask 077
if test -e .env; then
  existing=$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)
  test "$existing" = "$password" || exit 31
fi
tmp=$(mktemp .env.XXXXXXXX)
trap 'rm -f "$tmp"' EXIT
printf 'POSTGRES_PASSWORD=%s\nPOSTGRES_PORT=%s\n' "$password" "$port" > "$tmp"
mv -f "$tmp" .env
docker compose -p dbserver up -d >/dev/null 2>&1 || exit 32
docker compose -p dbserver port postgres 5432 | grep -Eq ":${port}$" || exit 33
count=0
while [ "$count" -lt 30 ]; do
  if printf '%s\n' "$password" | docker compose -p dbserver exec -T postgres sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; psql -h 127.0.0.1 -U postgres -d postgres -Atqc "SELECT 1"' >/dev/null 2>&1; then exit 0; fi
  count=$((count + 1)); sleep 2
done
exit 34`
	_, err := r.run(ctx, n, script, password+"\n"+strconv.Itoa(n.PostgresPort)+"\n")
	return err
}

// SQL sends credentials and SQL over SSH stdin, rather than in process arguments.
func (r SSHRemote) SQL(ctx context.Context, n config.Node, masterPassword, query string) (string, error) {
	const script = `set -eu
cd "$HOME/stacks/dbserver"
docker compose -p dbserver exec -T postgres sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql -h 127.0.0.1 -U postgres -d postgres -X -v ON_ERROR_STOP=1 -At'`
	return r.run(ctx, n, script, masterPassword+"\n"+query+"\n")
}
