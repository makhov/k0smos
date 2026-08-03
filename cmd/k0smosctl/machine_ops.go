package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/spf13/cobra"
)

func logsCmd() *cobra.Command {
	var (
		name   string
		follow bool
		lines  int
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show a machine's console",
		Long: `Prints the console of a guest started by "k0smosctl machine up".

The console is the only thing a k0smos node reports through — there is no shell and
no SSH — so this is how you watch a boot, and where k0s's own logs appear.`,
		Example: `  k0smosctl machine logs               # the whole console so far
  k0smosctl machine logs -f            # follow it
  k0smosctl machine logs -n 50         # the last 50 lines
  k0smosctl machine logs --name vm2 -f`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			console, _, _, err := guestPaths(name)
			if err != nil {
				return err
			}
			f, err := os.Open(console)
			if err != nil {
				if os.IsNotExist(err) {
					return fatalf("no console for guest %q — has it been booted? (%s)", name, console)
				}
				return err
			}
			defer f.Close()

			if lines > 0 {
				if err := seekToLastLines(f, lines); err != nil {
					return err
				}
			}
			if _, err := io.Copy(cmd.OutOrStdout(), f); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			return followFile(cmd.Context(), f, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", defaultGuestName, "which guest")
	f.BoolVarP(&follow, "follow", "f", false, "keep printing as the guest writes more")
	f.IntVarP(&lines, "lines", "n", 0, "start from the last N lines instead of the beginning")
	return cmd
}

// seekToLastLines positions f so that only the last n lines remain to be read.
//
// It reads backwards in blocks rather than loading the file: a k0s console reaches
// hundreds of kilobytes within a minute, and `logs -n 20` should not have to hold
// all of it.
func seekToLastLines(f *os.File, n int) error {
	const block = 32 << 10
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	buf := make([]byte, block)
	newlines, pos := 0, size
	for pos > 0 {
		read := int64(block)
		if pos < read {
			read = pos
		}
		pos -= read
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		chunk := buf[:read]
		if _, err := io.ReadFull(f, chunk); err != nil {
			return err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] != '\n' {
				continue
			}
			newlines++
			// n+1 newlines back is the start of the nth-from-last line, since the
			// final line usually ends with one.
			if newlines > n {
				_, err := f.Seek(pos+int64(i)+1, io.SeekStart)
				return err
			}
		}
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}

// followFile copies new data as it is written, like `tail -f`.
//
// Polling rather than a filesystem watcher: QEMU appends to this file steadily,
// the interval is imperceptible, and it needs no platform-specific machinery.
func followFile(ctx interface{ Done() <-chan struct{} }, f *os.File, out io.Writer) error {
	const poll = 200 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
		if _, err := io.Copy(out, f); err != nil {
			return err
		}
	}
}

func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List machines and whether they are running",
		Long: `Lists the guests "k0smosctl machine up" has started, and whether each is still up.

Liveness comes from its control socket answering, not from the recorded pid: a pid
can be reused, and a socket that answers is the thing the other subcommands
actually need.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			guests, err := listGuests()
			if err != nil {
				return err
			}
			if len(guests) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no guests; start one with `k0smosctl machine up`")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATE\tAPI\tSTARTED\tDISK")
			for _, g := range guests {
				_, socket, _, err := guestPaths(g.Name)
				state := "stopped"
				if err == nil && guestIsLive(socket) {
					state = "running"
				}
				api := "-"
				if g.APIPort != 0 {
					api = fmt.Sprintf(":%d", g.APIPort)
				}
				started := "-"
				if !g.Started.IsZero() {
					started = g.Started.Local().Format("15:04:05")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", g.Name, state, api, started, orDash(g.Disk))
			}
			return w.Flush()
		},
	}
	return cmd
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

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
