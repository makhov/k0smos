package main

import (
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/spf13/cobra"
)

func tokenCmd() *cobra.Command {
	var (
		name    string
		socket  string
		role    string
		out     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Mint a join token so another machine can join the cluster",
		Long: `Asks a running node to create a k0s join token.

A join token is signed with the cluster CA, so only a machine already in the
cluster can produce one — which is why this goes to the node rather than being
computed here. Hand the result to the joining machine as a file, and point k0s at
it with --token-file.

Minting one waits on the API server, so a node that has only just started may take
a while to answer; raise --timeout rather than retrying.

The same caution as kubeconfig applies: a controller token confers control-plane
membership on whoever holds it.`,
		Example: `  # grow the control plane: mint a token, put it on the new node's drive
  k0smosctl cluster token --role controller -o join-token
  k0smosctl gen --file join-token:/etc/k0s/join-token -o node2.iso
  k0smosctl machine up --name node2 --cidata node2.iso

  # print it instead of writing a file
  k0smosctl cluster token --role worker -o -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !slices.Contains(joinRoles, role) {
				return fmt.Errorf("--role must be one of %v, got %q", joinRoles, role)
			}
			socket, err := resolveSocket(socket, name)
			if err != nil {
				return err
			}
			data, err := request(socket, control.RequestToken+" "+role, timeout)
			if err != nil {
				return err
			}
			if out == "-" {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
				return err
			}
			// 0600: this is a credential for joining the control plane.
			if err := os.WriteFile(out, data, 0600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes, role %s)\n", out, len(data), role)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", defaultGuestName, "which guest")
	f.StringVar(&socket, "socket", "", "control socket path, instead of resolving --name")
	f.StringVar(&role, "role", "worker", "what the joining machine will be: controller or worker")
	f.StringVarP(&out, "output", "o", "join-token", `where to write it, or "-" for stdout`)
	// Longer than the other requests: the node runs k0s token create, which
	// contacts the API server and is slow on a cluster that is still coming up.
	f.DurationVar(&timeout, "timeout", 2*time.Minute, "how long to wait for the node to answer")
	return cmd
}

// joinRoles are the roles a token can be minted for. Checked here as well as in
// the guest so a typo fails at once rather than after a round trip.
var joinRoles = []string{"controller", "worker"}
