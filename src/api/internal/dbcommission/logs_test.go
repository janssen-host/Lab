package dbcommission

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llllogan/lab-controller/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestStreamLogsOverSSH(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(`CREATE TABLE ssh_host_keys(address TEXT PRIMARY KEY,public_key BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
		cfg.AddHostKey(hostSigner)
		server, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(reqs)
		for ch := range chans {
			channel, requests, err := ch.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				for req := range requests {
					if req.Type != "exec" {
						req.Reply(false, nil)
						continue
					}
					req.Reply(true, nil)
					data, _ := io.ReadAll(channel)
					if !strings.Contains(string(data), "container-1") {
						return
					}
					_, _ = io.WriteString(channel, "READY\nstdout line\n")
					_, _ = io.WriteString(channel.Stderr(), "stderr line\n")
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				}
			}()
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	remote := SSHRemote{KeyPath: keyPath, DB: db}
	stream, err := remote.StreamLogs(context.Background(), config.Node{Hostname: "127.0.0.1", SSHPort: address.Port}, "app", "container-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stdout line\n") || !strings.Contains(string(data), "stderr line\n") {
		t.Fatalf("missing log output: %q", data)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("SSH server did not stop")
	}
}
