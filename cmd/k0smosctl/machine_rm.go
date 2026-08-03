package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func rmCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "rm",
		Short: "Discard a stopped machine and its disk",
		Long: `Deletes a guest's state directory: its root disk, console and metadata.

The next "machine up" with that name starts again from a fresh clone of the image, which is
how a k0smos node is meant to be treated — replaced rather than repaired.

Refuses while the guest is still running, since removing a disk from under QEMU
corrupts it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := guestDir(name)
			if err != nil {
				return err
			}
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return fatalf("no guest named %q", name)
			}
			_, socket, _, err := guestPaths(name)
			if err != nil {
				return err
			}
			if guestIsLive(socket) {
				return fatalf("guest %q is still running — stop it first with "+
					"`k0smosctl machine shutdown --name %s`", name, name)
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed guest %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", defaultGuestName, "which guest")
	return cmd
}
