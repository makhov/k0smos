package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/amakhov/k0smos/internal/nethub"
	"github.com/spf13/cobra"
)

// clusterHubCmd is an implementation detail of cluster create. It is a separate
// process because QEMU's socket network needs the hub for the cluster's whole
// lifetime, while create must return once Kubernetes is ready.
func clusterHubCmd() *cobra.Command {
	var listen, ready string
	cmd := &cobra.Command{
		Use:    "__hub",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := nethub.Listen(listen)
			if err != nil {
				return err
			}
			defer h.Close()
			h.OnDrop = func(err error) { fmt.Fprintf(cmd.ErrOrStderr(), "cluster network: %v\n", err) }
			if err := os.WriteFile(ready, []byte(h.Addr()+"\n"), 0600); err != nil {
				return err
			}
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)
			<-sig
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "listen address")
	cmd.Flags().StringVar(&ready, "ready-file", "", "write the selected address here")
	_ = cmd.MarkFlagRequired("ready-file")
	return cmd
}

func startClusterHub(dir string) (string, int, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, err
	}
	ready := filepath.Join(dir, "hub.ready")
	_ = os.Remove(ready)
	logPath := filepath.Join(dir, "hub.log")
	log, err := os.Create(logPath)
	if err != nil {
		return "", 0, err
	}
	defer log.Close()
	child := exec.Command(exe, "__hub", "--ready-file", ready)
	child.Stdout, child.Stderr, child.Stdin = log, log, nil
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return "", 0, err
	}
	pid := child.Process.Pid
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(ready)
		if err == nil {
			addr := strings.TrimSpace(string(b))
			if _, _, err := net.SplitHostPort(addr); err == nil {
				if err := child.Process.Release(); err != nil {
					_ = child.Process.Kill()
					return "", 0, err
				}
				return addr, pid, nil
			}
		}
		if err := syscall.Kill(pid, 0); err != nil {
			body, _ := os.ReadFile(logPath)
			return "", 0, fmt.Errorf("cluster network hub exited: %s", strings.TrimSpace(string(body)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = child.Process.Kill()
	return "", 0, errors.New("cluster network hub did not become ready")
}
