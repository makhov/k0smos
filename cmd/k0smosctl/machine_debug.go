package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/amakhov/k0smos/internal/status"
)

// The read-only diagnostics. A k0smos machine has no shell, so the console is
// otherwise the only thing it reports through — fine for watching a boot, no use
// for asking a question about one. These three make a running machine
// answerable: what its init decided, what the kernel saw, and what is in a file.

// debugFlags are the flags every diagnostic command shares.
type debugFlags struct {
	name    string
	socket  string
	timeout time.Duration
}

func (d *debugFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&d.name, "name", defaultGuestName, "which machine")
	f.StringVar(&d.socket, "socket", "", "control socket path, instead of resolving --name")
	f.DurationVar(&d.timeout, "timeout", 10*time.Second, "how long to wait for the machine to answer")
}

// ask sends one request to the machine and returns its reply.
func (d *debugFlags) ask(req string) ([]byte, error) {
	socket, err := resolveSocket(d.socket, d.name)
	if err != nil {
		return nil, err
	}
	return request(socket, req, d.timeout)
}

func machineStatusCmd() *cobra.Command {
	var d debugFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what a machine's init decided during boot",
		Long: `Asks a running machine for its boot record.

The console shows a boot as it happens and then loses it: it scrolls away, does
not survive a reboot, and cannot be asked a question afterwards. This reports the
same conclusions durably — which root and data devices were chosen, which modules
loaded, whether a configuration drive was found, and how the supervised workload
is faring, including how many times it has restarted.

The record is also written to /run/k0smos/boot.json inside the machine, so it can
be read off the disk when the machine will not boot far enough to answer.`,
		Example: `  k0smosctl machine status
  k0smosctl machine status --name vm2
  k0smosctl machine status --json | jq .steps`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := d.ask(control.RequestStatus)
			if err != nil {
				return err
			}
			if asJSON {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			var rec status.Record
			if err := json.Unmarshal(data, &rec); err != nil {
				// Show what arrived rather than hiding it behind a parse error:
				// an older machine may answer with a shape this build predates.
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), status.Text(rec))
			return nil
		},
	}
	d.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the raw record instead of a summary")
	return cmd
}

func machineDmesgCmd() *cobra.Command {
	var d debugFlags
	cmd := &cobra.Command{
		Use:   "dmesg",
		Short: "Show a machine's kernel ring buffer",
		Long: `Asks a running machine for its kernel ring buffer.

Kernel messages do not reach the console when console= is wrong or the failure
precedes PID 1, and on real hardware they are where driver, disk-controller and
firmware problems appear. This reads the buffer the kernel is holding, so it
works even when nothing useful was ever printed to a serial line.`,
		Example: `  k0smosctl machine dmesg
  k0smosctl machine dmesg | grep -i virtio`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := d.ask(control.RequestDmesg)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	d.bind(cmd)
	return cmd
}

func machineCatCmd() *cobra.Command {
	var d debugFlags
	var out string
	cmd := &cobra.Command{
		Use:   "cat <path>",
		Short: "Read a file from a running machine",
		Long: `Reads one file from a machine over the control port.

This is how the evidence that is not on the console gets out of a machine with no
shell: container logs under /var/log/pods, the k0s configuration as it was
actually rendered, /run state, /etc/resolv.conf. Previously this meant shutting
the machine down and reading its disk.

The path must be absolute. Directories are refused, as is anything too large for
one reply — so an oversized file is reported by name and size rather than
arriving truncated.

Note this makes the control port a general file-read channel. That is not a new
exposure — whoever can write to it already obtains cluster-admin and can stop the
machine — but the port should not be exposed anywhere the disk is not.`,
		Example: `  k0smosctl machine cat /etc/k0s/k0s.yaml
  k0smosctl machine cat /run/k0smos/boot.json
  k0smosctl machine cat /var/log/pods/kube-system_kube-proxy-x/kube-proxy/0.log

  # write it out instead of printing
  k0smosctl machine cat /etc/k0s/k0s.yaml -o k0s.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := d.ask(control.RequestCat + " " + args[0])
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			return os.WriteFile(out, data, 0644)
		},
	}
	d.bind(cmd)
	cmd.Flags().StringVarP(&out, "output", "o", "", `where to write it, or "-" for stdout`)
	return cmd
}
