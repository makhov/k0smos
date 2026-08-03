package main

import (
	"fmt"
	"time"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/spf13/cobra"
)

// shutdownCmd builds the shutdown and reboot commands, which differ only in the
// word they send.
func shutdownCmd(verb string) *cobra.Command {
	var (
		name    string
		socket  string
		timeout time.Duration
	)
	word := control.PowerOff.String()
	short := "Shut a running machine down cleanly"
	if verb == "reboot" {
		word = control.Reboot.String()
		short = "Restart a running machine cleanly"
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
			data, err := request(socket, word, timeout)
			if err != nil {
				return fmt.Errorf("guest did not acknowledge %s: %w", word, err)
			}
			if len(data) != 0 {
				return fmt.Errorf("unexpected %d-byte reply to %s", len(data), word)
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
