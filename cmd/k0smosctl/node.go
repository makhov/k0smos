package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amakhov/k0smos/internal/control"
)

// defaultSocket is where image/run-qemu.sh puts the control socket. QEMU listens
// on it and relays to the guest's virtio-serial port, so the host connects as a
// client.
const defaultSocket = "dist/control.sock"

func kubeconfigCmd() *cobra.Command {
	var (
		socket  string
		out     string
		server  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Fetch the admin kubeconfig from a running node",
		Long: `Asks a running node for its admin kubeconfig over the control port.

This replaces reading the guest's disk offline with debugfs, which meant shutting
the machine down first and, on macOS, a Docker container to supply e2fsprogs.

The node reads the file off its filesystem, so this works whether or not k0s is
still running, and says so plainly when the cluster has not created it yet.

Note that whoever can write to the control port obtains cluster-admin. That is not
a new exposure — the same channel can stop the machine — but the port should not be
exposed anywhere the disk is not.`,
		Example: `  k0smosctl kubeconfig -o kubeconfig
  KUBECONFIG=kubeconfig kubectl get nodes

  # print it instead of writing a file
  k0smosctl kubeconfig -o -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := request(socket, control.RequestKubeconfig, timeout)
			if err != nil {
				return err
			}
			// k0s writes the server as localhost, which is right on the node and
			// wrong everywhere else. run-qemu.sh forwards 6443 to the host, so
			// rewriting it makes the file usable as fetched.
			if server != "" {
				data = []byte(strings.ReplaceAll(string(data), "https://localhost:", "https://"+server+":"))
			}
			if out == "-" {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			// 0600: this is a cluster-admin credential.
			if err := os.WriteFile(out, data, 0600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes)\n", out, len(data))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&socket, "socket", defaultSocket, "control socket of the running guest")
	f.StringVarP(&out, "output", "o", "kubeconfig", `where to write it, or "-" for stdout`)
	f.StringVar(&server, "server", "127.0.0.1", `rewrite the API server host; "" keeps what the node wrote`)
	f.DurationVar(&timeout, "timeout", 10*time.Second, "how long to wait for the node to answer")
	return cmd
}

// shutdownCmd builds the shutdown and reboot commands, which differ only in the
// word they send.
func shutdownCmd(name string) *cobra.Command {
	var (
		socket  string
		timeout time.Duration
	)
	word := control.PowerOff.String()
	short := "Shut a running node down cleanly"
	if name == "reboot" {
		word = control.Reboot.String()
		short = "Restart a running node cleanly"
	}
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Long: short + `.

Use this rather than killing QEMU: a hard kill leaves the ext4 root with an
unreplayed journal, which loses recent writes and makes the image read as empty
afterwards.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := dial(socket, timeout)
			if err != nil {
				return err
			}
			defer conn.Close()
			if _, err := fmt.Fprintf(conn, "%s\n", word); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sent %s to %s\n", word, socket)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&socket, "socket", defaultSocket, "control socket of the running guest")
	f.DurationVar(&timeout, "timeout", 5*time.Second, "how long to wait for the socket")
	return cmd
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
