package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

type clusterMachine struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	IP      string `json:"ip"`
	APIPort int    `json:"apiPort,omitempty"`
}

type clusterMeta struct {
	Name       string           `json:"name"`
	HubPID     int              `json:"hubPid"`
	HubAddress string           `json:"hubAddress"`
	Machines   []clusterMachine `json:"machines"`
	Created    time.Time        `json:"created"`
}

var clusterNamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

func clusterNodeIP(i int) string { return fmt.Sprintf("%s.%d", clusterSubnet, clusterFirstHost+i) }

func clusterNodeMAC(i int) string { return fmt.Sprintf("52:54:00:c0:5e:%02x", clusterFirstHost+i) }

func clusterStateDir(name string) (string, error) {
	if err := validGuestName(name); err != nil {
		return "", fmt.Errorf("invalid cluster name: %w", err)
	}
	root, err := stateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".clusters", name), nil
}
