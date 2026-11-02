package config

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"strings"
)

func Read(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err = decoder.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = decoder.Decode(&extra); err == nil {
		return c, errors.New("multiple YAML documents are not supported")
	} else if !errors.Is(err, io.EOF) {
		return c, err
	}
	seen := map[string]bool{}
	for _, group := range [][]Node{c.WorkloadHosts, c.DatabaseHosts} {
		for _, n := range group {
			if strings.TrimSpace(n.Name) == "" || strings.TrimSpace(n.Hostname) == "" {
				return c, errors.New("node name and hostname are required")
			}
			if seen[n.Name] {
				return c, fmt.Errorf("duplicate node name %q", n.Name)
			}
			seen[n.Name] = true
			if n.SSHPort < 1 || n.SSHPort > 65535 {
				return c, fmt.Errorf("invalid SSH port for %q", n.Name)
			}
		}
	}
	for _, n := range c.DatabaseHosts {
		if n.PostgresPort < 1 || n.PostgresPort > 65535 {
			return c, fmt.Errorf("invalid PostgreSQL port for %q", n.Name)
		}
	}
	for _, n := range c.WorkloadHosts {
		if n.PostgresPort != 0 {
			return c, fmt.Errorf("postgres_port is only valid for database host %q", n.Name)
		}
	}
	return c, nil
}
