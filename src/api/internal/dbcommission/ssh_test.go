package dbcommission

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestHostKeysAreRememberedAndChangesRejected(t *testing.T) {
	db := newTestDB(t)
	if _, err := New(db, strings.Repeat("ab", 32), &fakeRemote{}); err != nil {
		t.Fatal(err)
	}
	remote := SSHRemote{DB: db}
	makeKey := func() ssh.PublicKey {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, err := ssh.NewPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	first, changed := makeKey(), makeKey()
	if err := remote.verifyHostKey("10.0.0.1:22", nil, first); err != nil {
		t.Fatal(err)
	}
	if err := remote.verifyHostKey("10.0.0.1:22", nil, first); err != nil {
		t.Fatal(err)
	}
	if err := remote.verifyHostKey("10.0.0.1:22", nil, changed); err == nil {
		t.Fatal("changed host key accepted")
	}
	if err := remote.verifyHostKey("10.0.0.2:22", nil, changed); err != nil {
		t.Fatal(err)
	}
}
