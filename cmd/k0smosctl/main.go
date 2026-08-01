// Command k0smosctl drives k0smos nodes from the host.
//
// It exists because the routine tasks each needed an external tool and, on macOS,
// Docker to supply it: xorriso to build a configuration drive, debugfs to read a
// kubeconfig off a stopped guest's disk. k0smos already knows both formats and
// already has a channel to the node, so neither is necessary.
//
// This runs on the host, not the node, so it must build for darwin as well as
// linux: nothing here may reach for anything Linux-only. Cobra is a dependency of
// this command alone — the node binary must stay as small and as dependency-free
// as it is.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is stamped at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if err := root().Execute(); err != nil {
		// Printed here because SilenceErrors stops cobra doing it. Without this
		// the command exited 1 with nothing on stderr, which is the worst of both
		// — a mistyped flag looked like a crash.
		fmt.Fprintln(os.Stderr, "k0smosctl: "+err.Error())
		os.Exit(1)
	}
}

func root() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k0smosctl",
		Short: "Build configuration drives for k0smos nodes and talk to running ones",
		Long: `k0smosctl drives k0smos nodes from the host.

A k0smos node has no shell and no SSH. It is configured by a cloud-init drive
before it boots, and answers a small set of requests over a virtio-serial control
port while it runs.`,
		// Errors are printed by main, once, with a consistent prefix. Usage is
		// suppressed so a runtime failure does not bury its message under the
		// full help text; a flag error still prints usage, because cobra treats
		// those separately.
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetErrPrefix("k0smosctl:")
	cmd.AddCommand(genCmd(), bootCmd(), logsCmd(), listCmd(), kubeconfigCmd(),
		tokenCmd(), shutdownCmd("shutdown"), shutdownCmd("reboot"), rmCmd())
	return cmd
}

// fatalf reports a problem the way a CLI should: to stderr, without a stack.
func fatalf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
