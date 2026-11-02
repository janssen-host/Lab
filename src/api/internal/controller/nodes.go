package controller

import "github.com/llllogan/lab-controller/internal/config"

type nodeStatus struct {
	config.Node
	Type                   string `json:"type"`
	Reachable              bool   `json:"reachable"`
	CheckedAt              string `json:"checked_at"`
	Error                  string `json:"error,omitempty"`
	CommissioningStatus    string `json:"commissioning_status,omitempty"`
	CommissioningError     string `json:"commissioning_error,omitempty"`
	CommissioningCheckedAt string `json:"commissioning_checked_at,omitempty"`
}
