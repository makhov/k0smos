package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amakhov/k0smos/internal/iso9660"
	"github.com/amakhov/k0smos/internal/metadata"
)

func genCmd() *cobra.Command {
	var (
		out      string
		userData string
		hostname string
		instance string
		label    string
		files    []string
	)
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Write a cloud-init drive for a node to boot with",
		Long: `Writes a NoCloud cloud-init drive: user-data and meta-data at the image root,
with Rock Ridge names so "user-data" survives intact.

The ISO is written directly, so no xorriso — and on macOS no Docker — is needed.
What it generates is parsed before being written, so a mistake surfaces here
rather than as a console warning after the machine has already booted.`,
		Example: `  # place files on the node, keeping their permissions
  k0smosctl gen --file k0s.yaml:/etc/k0s/k0s.yaml --hostname node-1

  # a cloud-config rendered elsewhere ("-" reads stdin)
  k0smosctl gen --user-data cloud-config.yaml --hostname node-1

  # a manifest k0s applies on its first reconcile
  k0smosctl gen --file ns.yaml:/var/lib/k0s/manifests/demo/ns.yaml

  # then boot with it attached
  k0smosctl machine up --cidata cidata.iso`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := userDataBody(userData, files, cmd.InOrStdin())
			if err != nil {
				return err
			}

			var meta strings.Builder
			fmt.Fprintf(&meta, "instance-id: %s\n", instance)
			if hostname != "" {
				fmt.Fprintf(&meta, "local-hostname: %s\n", hostname)
			}

			// Checked with the same parser the node uses, so a drive that would
			// be ignored is rejected here instead of booting into a machine that
			// silently comes up unconfigured.
			ud, _, err := metadata.Load(inMemory{
				"user-data": body,
				"meta-data": []byte(meta.String()),
			})
			if err != nil {
				return fatalf("the user-data does not parse: %w", err)
			}
			for _, w := range ud.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			// Warnings alone are not fatal — one skipped runcmd among many is a
			// choice the caller can make. Producing *nothing* is not: that drive
			// has no effect, and the commonest cause is user-data missing its
			// "#cloud-config" first line, which k0smos ignores by design.
			if len(ud.WriteFiles) == 0 && len(ud.RunCmd) == 0 && len(ud.Warnings) > 0 {
				return fatalf("this user-data would have no effect on the node; " +
					"cloud-config must begin with a #cloud-config line")
			}

			f, err := os.Create(out)
			if err != nil {
				return err
			}
			defer f.Close()
			err = iso9660.Write(f, label, []iso9660.File{
				{Name: "user-data", Data: body},
				{Name: "meta-data", Data: []byte(meta.String())},
			})
			if err != nil {
				return err
			}
			info, err := f.Stat()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes, LABEL=%s)\n", out, info.Size(), label)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&out, "output", "o", "cidata.iso", "output image path")
	f.StringVar(&userData, "user-data", "", `cloud-config file to use as-is, or "-" for stdin`)
	f.StringVar(&hostname, "hostname", "", "local-hostname for the node")
	f.StringVar(&instance, "instance-id", "k0smos", "instance-id for meta-data")
	f.StringVar(&label, "label", metadata.NoCloudLabel, "volume label k0smos looks for")
	f.StringArrayVar(&files, "file", nil, "place a host file on the node, as SRC:DEST (repeatable)")
	return cmd
}

// userDataBody returns the cloud-config to place on the drive: either one supplied
// wholesale, or one synthesised from --file arguments.
func userDataBody(path string, files []string, stdin io.Reader) ([]byte, error) {
	switch {
	case path != "" && len(files) > 0:
		return nil, errors.New("--user-data and --file cannot be combined; put the files in the cloud-config")
	case path == "-":
		return io.ReadAll(stdin)
	case path != "":
		return os.ReadFile(path)
	case len(files) > 0:
		return renderWriteFiles(files)
	default:
		return nil, errors.New("nothing to configure: pass --user-data or --file")
	}
}

// renderWriteFiles turns SRC:DEST pairs into a write_files cloud-config.
//
// Content is base64 encoded rather than inlined, so a file containing anything
// YAML would reinterpret — tabs, colons, leading dashes — survives intact, and
// there are no quoting or indentation rules to get wrong here.
func renderWriteFiles(files []string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("#cloud-config\nwrite_files:\n")
	for _, spec := range files {
		src, dest, ok := strings.Cut(spec, ":")
		if !ok || src == "" || dest == "" {
			return nil, fmt.Errorf("--file %q is not SRC:DEST", spec)
		}
		if !strings.HasPrefix(dest, "/") {
			return nil, fmt.Errorf("--file %q: the destination must be an absolute path", spec)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(src)
		if err != nil {
			return nil, err
		}
		// The source file's permissions carry across, so a token or a key lands on
		// the node no more readable than it was here.
		fmt.Fprintf(&b, "  - path: %s\n    permissions: %q\n    encoding: b64\n    content: %s\n",
			dest, fmt.Sprintf("%04o", info.Mode().Perm()),
			base64.StdEncoding.EncodeToString(data))
	}
	return []byte(b.String()), nil
}

// inMemory lets the generated pair be validated through metadata.Load without
// touching a disk.
type inMemory map[string][]byte

func (m inMemory) ReadFile(name string) ([]byte, error) {
	if b, ok := m[filepath.ToSlash(name)]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}
