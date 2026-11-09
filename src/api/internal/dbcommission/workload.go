package dbcommission

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/llllogan/lab-controller/internal/config"
)

func projectName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "lab-" + hex.EncodeToString(sum[:16])
}

const stackDeployScript = `set -eu
IFS= read -r name
IFS= read -r project
path="$HOME/stacks/$name"
if [ -L "$path" ]; then exit 40; fi
mkdir -p -- "$path"
cd "$path"
if { [ -e docker-compose.yml ] && [ ! -f .env ]; } || { [ -e .env ] && [ ! -f docker-compose.yml ]; }; then exit 40; fi
umask 077
stage=$(mktemp -d .deploy.XXXXXXXX)
trap 'rm -rf -- "$stage"' EXIT
mkdir "$stage/new" "$stage/old"
tar -xf - -C "$stage/new"
old=0
if [ -f docker-compose.yml ]; then
  old=1
  cp -p docker-compose.yml .env "$stage/old/"
fi
started=0
rollback() {
  if [ "$started" -eq 1 ]; then docker compose -p "$project" down --remove-orphans >/dev/null 2>&1 || return 1; fi
  if [ "$old" -eq 1 ]; then
    cp -p "$stage/old/docker-compose.yml" "$stage/old/.env" .
    docker compose -p "$project" up -d --wait --wait-timeout 60 >/dev/null 2>&1
  else
    rm -f docker-compose.yml .env
  fi
}
if ! cp -p "$stage/new/docker-compose.yml" "$stage/new/.env" .; then
  if ! rollback; then exit 42; fi
  exit 41
fi
if ! docker compose -p "$project" config -q >/dev/null 2>&1; then
  if ! rollback; then exit 42; fi
  exit 41
fi
if ! docker compose -p "$project" pull >/dev/null 2>&1; then
  if ! rollback; then exit 42; fi
  exit 41
fi
started=1
if ! docker compose -p "$project" up -d --wait --wait-timeout 60 --remove-orphans >/dev/null 2>&1; then
  if ! rollback; then exit 42; fi
  exit 41
fi`

func (r SSHRemote) DeployStack(ctx context.Context, node config.Node, name string, compose, dotenv []byte) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, file := range []struct {
		name string
		data []byte
	}{{"docker-compose.yml", compose}, {".env", dotenv}} {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0600, Size: int64(len(file.data))}); err != nil {
			return err
		}
		if _, err := writer.Write(file.data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}

	_, err := r.run(ctx, node, stackDeployScript, name+"\n"+projectName(name)+"\n"+archive.String())
	return err
}

func (r SSHRemote) StackStatus(ctx context.Context, node config.Node, name string) ([]json.RawMessage, error) {
	const script = `set -eu
IFS= read -r name
IFS= read -r project
cd "$HOME/stacks/$name"
docker compose -p "$project" ps --all --format json`
	output, err := r.run(ctx, node, script, name+"\n"+projectName(name)+"\n")
	if err != nil {
		return nil, err
	}
	result := []json.RawMessage{}
	if strings.TrimSpace(output) == "" {
		return result, nil
	}
	if strings.HasPrefix(strings.TrimSpace(output), "[") {
		if err = json.Unmarshal([]byte(output), &result); err != nil {
			return nil, fmt.Errorf("invalid Compose status: %w", err)
		}
		return result, nil
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	for decoder.More() {
		var item json.RawMessage
		if err = decoder.Decode(&item); err != nil {
			return nil, fmt.Errorf("invalid Compose status: %w", err)
		}
		result = append(result, item)
	}
	return result, nil
}

// RestartContainer restarts only a container belonging to the requested stack.
func (r SSHRemote) RestartContainer(ctx context.Context, node config.Node, stack, container string) error {
	const script = `set -eu
IFS= read -r project
IFS= read -r container
actual=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' -- "$container" 2>/dev/null) || exit 1
[ "$actual" = "$project" ] || exit 1
docker restart -- "$container" >/dev/null`
	_, err := r.run(ctx, node, script, projectName(stack)+"\n"+container+"\n")
	return err
}

// UpdateStack pulls configured images, starts the updated stack and removes
// orphan containers and dangling images. Volumes are never removed.
func (r SSHRemote) UpdateStack(ctx context.Context, node config.Node, name string) error {
	const script = `set -eu
IFS= read -r name
IFS= read -r project
cd "$HOME/stacks/$name"
docker compose -p "$project" pull
docker compose -p "$project" up -d --wait --wait-timeout 60 --remove-orphans
docker image prune -f`
	_, err := r.run(ctx, node, script, name+"\n"+projectName(name)+"\n")
	return err
}

// StackServices returns the services defined by the deployed Compose configuration.
func (r SSHRemote) StackServices(ctx context.Context, node config.Node, name string) ([]string, error) {
	const script = `set -eu
IFS= read -r name
IFS= read -r project
cd "$HOME/stacks/$name"
docker compose -p "$project" config --services`
	output, err := r.run(ctx, node, script, name+"\n"+projectName(name)+"\n")
	if err != nil {
		return nil, err
	}
	return strings.Fields(output), nil
}

// DeleteStack removes the Compose project and its local configuration.
func (r SSHRemote) DeleteStack(ctx context.Context, node config.Node, name string) error {
	const script = `set -eu
IFS= read -r name
IFS= read -r project
path="$HOME/stacks/$name"
[ ! -L "$path" ] || exit 1
cd "$path"
docker compose -p "$project" down --remove-orphans --volumes --rmi all
cd "$HOME/stacks"
rm -rf -- "$name"`
	_, err := r.run(ctx, node, script, name+"\n"+projectName(name)+"\n")
	return err
}
