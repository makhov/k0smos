package main

import (
	"fmt"
	"net"
	"time"

	"github.com/amakhov/k0smos/internal/control"
)

// The host side of a node's control port. A k0smos node has no shell and no
// SSH; it answers a small set of requests on a virtio-serial port instead.
// Both the machine commands (shutdown, reboot) and the cluster commands
// (kubeconfig, token) reach a node through here.

// resolveSocket picks the control socket to talk to: an explicit path wins,
// otherwise the named guest's. QEMU listens on it and relays to the guest's
// virtio-serial port, so the host connects as a client.
func resolveSocket(socket, name string) (string, error) {
	if socket == "" {
		_, resolved, _, err := guestPaths(name)
		if err != nil {
			return "", err
		}
		socket = resolved
	}
	return socket, checkSocketPath(socket)
}

func dial(socket string, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return nil, fmt.Errorf("no control socket at %s — is the guest running? (%w)", socket, err)
	}
	return conn, nil
}

// request performs one request/response exchange against a node.
func request(socket, name string, timeout time.Duration) ([]byte, error) {
	conn, err := dial(socket, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// A node that never answers must not hang the CLI: the port is reopened by
	// the guest on EOF, so a request sent while it is between opens is simply
	// lost, and waiting forever would hide that.
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	return control.Request(conn, name)
}
