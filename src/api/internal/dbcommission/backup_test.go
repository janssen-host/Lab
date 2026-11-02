package dbcommission

import (
	"bytes"
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

	"github.com/llllogan/lab-controller/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestDumpStreamsOverSSH(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(`CREATE TABLE ssh_host_keys(address TEXT PRIMARY KEY,public_key BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: data}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
		cfg.AddHostKey(signer)
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
					var cmd struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &cmd)
					req.Reply(true, nil)
					input, _ := io.ReadAll(channel)
					received <- cmd.Command + "\nINPUT=" + string(input)
					_, _ = channel.Write([]byte("PGDMP-example"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	remote := SSHRemote{KeyPath: keyPath, DB: db}
	var output bytes.Buffer
	if err = remote.Dump(context.Background(), config.Node{Hostname: "127.0.0.1", SSHPort: addr.Port}, "secret-password", "test_db", &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "PGDMP-example" {
		t.Fatalf("dump: %q", output.String())
	}
	request := <-received
	parts := strings.SplitN(request, "\nINPUT=", 2)
	if len(parts) != 2 || strings.Contains(parts[0], "secret-password") || parts[1] != "secret-password\ntest_db\n" || !strings.Contains(parts[0], "pg_dump") {
		t.Fatal("unexpected SSH command or stdin")
	}
}
