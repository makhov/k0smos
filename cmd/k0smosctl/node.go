package main

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/amakhov/k0smos/internal/control"
)

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

func kubeconfigCmd() *cobra.Command {
	var (
		name    string
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
  k0smosctl kubeconfig -o -

  # a guest booted with --name vm2 --api-port 7443
  k0smosctl kubeconfig --name vm2 --server 127.0.0.1:7443 -o kubeconfig2`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			socket, err := resolveSocket(socket, name)
			if err != nil {
				return err
			}
			// A guest booted with a non-default --api-port is reached on that port,
			// and boot recorded it. Filling it in here is the difference between
			// this working and handing back a kubeconfig that points at whichever
			// guest happens to hold 6443.
			if !cmd.Flags().Changed("server") && server != "" {
				if port, err := recordedAPIPort(name); err == nil && port != 0 {
					server = fmt.Sprintf("%s:%d", server, port)
				}
			}
			data, err := request(socket, control.RequestKubeconfig, timeout)
			if err != nil {
				return err
			}
			// k0s writes the server as localhost:6443, which is right on the node
			// and wrong everywhere else. The port matters as much as the host: a
			// second guest is reached on a different forwarded port, and rewriting
			// only the host would point its kubeconfig at the first guest.
			if server != "" {
				out, err := rewriteServer(data, server)
				if err != nil {
					return err
				}
				data = out
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
	f.StringVar(&name, "name", defaultGuestName, "which guest")
	f.StringVar(&socket, "socket", "", "control socket path, instead of resolving --name")
	f.StringVarP(&out, "output", "o", "kubeconfig", `where to write it, or "-" for stdout`)
	f.StringVar(&server, "server", "127.0.0.1",
		`rewrite the API server as host or host:port; "" keeps what the node wrote`)
	f.DurationVar(&timeout, "timeout", 10*time.Second, "how long to wait for the node to answer")
	return cmd
}

// shutdownCmd builds the shutdown and reboot commands, which differ only in the
// word they send.
func shutdownCmd(verb string) *cobra.Command {
	var (
		name    string
		socket  string
		timeout time.Duration
	)
	word := control.PowerOff.String()
	short := "Shut a running node down cleanly"
	if verb == "reboot" {
		word = control.Reboot.String()
		short = "Restart a running node cleanly"
	}
	cmd := &cobra.Command{
		Use:   verb,
		Short: short,
		Long: short + `.

Use this rather than killing QEMU: a hard kill leaves the ext4 root with an
unreplayed journal, which loses recent writes and makes the image read as empty
afterwards.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			socket, err := resolveSocket(socket, name)
			if err != nil {
				return err
			}
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
	f.StringVar(&name, "name", defaultGuestName, "which guest")
	f.StringVar(&socket, "socket", "", "control socket path, instead of resolving --name")
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

// serverField matches the API server URL k0s writes into a kubeconfig.
var serverField = regexp.MustCompile(`(server:\s*https://)([^\s]+)`)

// rewriteServer points a kubeconfig at where the API server is reachable from
// here. server may be a host, keeping the port the node wrote, or host:port.
func rewriteServer(data []byte, server string) ([]byte, error) {
	if !serverField.Match(data) {
		return nil, fmt.Errorf("no API server URL found in the kubeconfig")
	}
	return serverField.ReplaceAllFunc(data, func(m []byte) []byte {
		g := serverField.FindSubmatch(m)
		prefix, authority := string(g[1]), string(g[2])
		host, port := server, ""
		if h, p, err := net.SplitHostPort(server); err == nil {
			host, port = h, ":"+p
		} else if _, p, err := net.SplitHostPort(authority); err == nil {
			// Keep the port the node wrote when none was asked for.
			port = ":" + p
		}
		return []byte(prefix + host + port)
	}), nil
}

// recordedAPIPort returns the host port boot forwarded for a named guest.
func recordedAPIPort(name string) (int, error) {
	_, _, metaPath, err := guestPaths(name)
	if err != nil {
		return 0, err
	}
	m, err := loadMeta(metaPath)
	if err != nil {
		return 0, err
	}
	return m.APIPort, nil
}
