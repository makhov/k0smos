package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/spf13/cobra"
)

func clusterRemoveCmd() *cobra.Command {
	var name string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "rm",
		Aliases: []string{"delete"},
		Short:   "Shut down and discard a local cluster",
		Long: `Shuts every machine down cleanly, stops the cluster's userspace network,
then removes the machine clones, config drives and recorded cluster state.

It refuses to remove disks if a machine does not shut down within the timeout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := clusterStateDir(name)
			if err != nil {
				return err
			}
			var state clusterMeta
			body, err := os.ReadFile(filepath.Join(dir, "cluster.json"))
			if os.IsNotExist(err) {
				return fmt.Errorf("no cluster named %q", name)
			}
			if err != nil {
				return err
			}
			if err := json.Unmarshal(body, &state); err != nil {
				return err
			}

			// Every machine is asked at once and each request is held until that
			// guest acks: one that has not opened its control port yet would lose
			// a command that was written and dropped.
			var (
				wg   sync.WaitGroup
				mu   sync.Mutex
				errs []error
			)
			for _, machine := range state.Machines {
				_, socket, metaPath, err := guestPaths(machine.Name)
				if err != nil {
					return err
				}
				meta, metaErr := loadMeta(metaPath)
				if metaErr != nil || !processRunning(meta.PID) {
					continue
				}
				wg.Add(1)
				go func(name, socket string) {
					defer wg.Done()
					if _, err := request(socket, control.PowerOff.String(), powerAckTimeout); err != nil {
						mu.Lock()
						errs = append(errs, fmt.Errorf("machine %q is running but did not accept a clean shutdown: %w", name, err))
						mu.Unlock()
					}
				}(machine.Name, socket)
			}
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				return err
			}

			deadline := time.Now().Add(timeout)
			for _, machine := range state.Machines {
				_, _, metaPath, _ := guestPaths(machine.Name)
				meta, err := loadMeta(metaPath)
				if err != nil {
					continue
				}
				for processRunning(meta.PID) && time.Now().Before(deadline) {
					time.Sleep(250 * time.Millisecond)
				}
				if processRunning(meta.PID) {
					return fmt.Errorf("machine %q did not stop within %s; no disks were removed", machine.Name, timeout)
				}
			}

			if state.HubPID > 0 {
				_ = syscall.Kill(state.HubPID, syscall.SIGTERM)
			}
			for _, machine := range state.Machines {
				machineDir, err := guestDir(machine.Name)
				if err != nil {
					return err
				}
				if err := os.RemoveAll(machineDir); err != nil {
					return err
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed cluster %q and %d machine(s)\n", name, len(state.Machines))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "dev", "cluster to remove")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "time allowed for clean machine shutdown")
	return cmd
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
