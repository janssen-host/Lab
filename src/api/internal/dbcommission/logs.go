package dbcommission

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
	"golang.org/x/crypto/ssh"
)

type logStream struct {
	io.Reader
	close func()
	once  sync.Once
}

func (s *logStream) Close() error { s.once.Do(s.close); return nil }

func (r SSHRemote) StreamLogs(ctx context.Context, n config.Node, stack, container string) (io.ReadCloser, error) {
	key, err := os.ReadFile(r.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}
	address := net.JoinHostPort(n.Hostname, strconv.Itoa(n.SSHPort))
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cc, chans, reqs, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "controller", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: r.verifyHostKey})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH connection: %w", err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}
	session.Stdin = strings.NewReader(projectName(stack) + "\n" + container + "\n")
	const script = `set -eu
IFS= read -r project
IFS= read -r container
actual=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' -- "$container" 2>/dev/null) || exit 1
[ "$actual" = "$project" ] || exit 1
printf 'READY\n'
exec docker logs --tail 500 --follow -- "$container"`
	if err = session.Start(script); err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("start logs: %w", err)
	}
	// Cancel a stalled remote inspection as well as an active log stream.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
			client.Close()
		case <-done:
		}
	}()
	reader := bufio.NewReader(stdout)
	ready, err := reader.ReadString('\n')
	if err != nil || ready != "READY\n" {
		close(done)
		session.Close()
		client.Close()
		return nil, fmt.Errorf("container not found in stack or Docker is unavailable")
	}
	_ = conn.SetDeadline(time.Time{})
	pipeR, pipeW := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(pipeW, reader) }()
	go func() { defer wg.Done(); _, _ = io.Copy(pipeW, stderr) }()
	go func() { wg.Wait(); err := session.Wait(); _ = pipeW.CloseWithError(err) }()
	stream := &logStream{Reader: pipeR}
	stream.close = func() { close(done); session.Close(); client.Close(); pipeR.Close() }
	return stream, nil
}
