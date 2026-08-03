package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

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
