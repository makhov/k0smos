package main

import (
	"io"
	"os"
	"time"

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
