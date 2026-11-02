package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReadConfigValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	valid := "workload_hosts:\n  - name: worker\n    hostname: localhost\n    ssh_port: " + strconv.Itoa(22) + "\ndatabase_hosts:\n  - name: db\n    hostname: localhost\n    ssh_port: 22\n    postgres_port: 5432\n"
	for _, tc := range []struct {
		name, content string
		wantErr       bool
	}{
		{"valid", valid, false},
		{"duplicate", valid + "  - name: worker\n    hostname: localhost\n    ssh_port: 22\n    postgres_port: 5432\n", true},
		{"unknown field", "workload_hosts:\n  - name: worker\n    hostname: localhost\n    ssh_port: 22\n    typo: true\n", true},
		{"missing port", "database_hosts:\n  - name: db\n    hostname: localhost\n    ssh_port: 22\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Read(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
