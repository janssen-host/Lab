package dbcommission

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedStackDeploymentRestoresPreviousFiles(t *testing.T) {
	home := t.TempDir()
	stackDir := filepath.Join(home, "stacks", "app")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(stackDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"docker-compose.yml": "old compose", ".env": "OLD=old", "env.json": "old json"} {
		if err := os.WriteFile(filepath.Join(stackDir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fake := `#!/bin/sh
case "$*" in
  *" up "*) if [ ! -e "$HOME/failed-once" ]; then touch "$HOME/failed-once"; exit 1; fi ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for name, data := range map[string]string{"docker-compose.yml": "new compose", ".env": "NEW=new"} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", stackDeployScript)
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader("app\nproject\n" + archive.String())
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 41 {
		t.Fatalf("expected deployment failure, got %v", err)
	}
	for name, want := range map[string]string{"docker-compose.yml": "old compose", ".env": "OLD=old", "env.json": "old json"} {
		got, err := os.ReadFile(filepath.Join(stackDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s was not restored: %q, %v", name, got, err)
		}
	}
}

func TestStackDeploymentPullsBeforeStarting(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/commands\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for name, data := range map[string]string{"docker-compose.yml": "compose", ".env": "ENV=value"} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", stackDeployScript)
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader("app\nproject\n" + archive.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deployment failed: %v: %s", err, output)
	}
	commands, err := os.ReadFile(filepath.Join(home, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	want := "compose -p project config -q\ncompose -p project pull\ncompose -p project up -d --wait --wait-timeout 60 --remove-orphans\n"
	if string(commands) != want {
		t.Fatalf("unexpected commands: %s", commands)
	}
}
