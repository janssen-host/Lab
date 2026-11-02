package config

type Node struct {
	Name         string `yaml:"name" json:"name"`
	Hostname     string `yaml:"hostname" json:"hostname"`
	SSHPort      int    `yaml:"ssh_port" json:"ssh_port"`
	PostgresPort int    `yaml:"postgres_port,omitempty" json:"postgres_port,omitempty"`
}
type Config struct {
	WorkloadHosts []Node `yaml:"workload_hosts"`
	DatabaseHosts []Node `yaml:"database_hosts"`
}
