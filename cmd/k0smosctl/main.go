// Command k0smosctl builds the things a k0smos node needs, from the host.
//
// It exists because configuring a node used to mean assembling an ISO by hand
// with xorriso — and on macOS, where there is no xorriso, that meant a Docker
// invocation. k0smos already knows the format well enough to read it off a block
// device, so it can write one too.
//
// This runs on the host, not the node, so it must build for darwin as well as
// linux: nothing here may reach for anything Linux-only.
package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/amakhov/k0smos/internal/iso9660"
	"github.com/amakhov/k0smos/internal/metadata"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "k0smosctl: "+err.Error())
		os.Exit(1)
	}
}

const usage = `k0smosctl builds configuration drives for k0smos nodes.

Usage:
  k0smosctl gen [flags]     write a cloud-init drive

Run a subcommand with -h for its flags.
`

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no subcommand given")
	}
	switch args[0] {
	case "gen":
		return gen(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func gen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	var (
		out       = fs.String("o", "cidata.iso", "output image path")
		userData  = fs.String("user-data", "", "path to a cloud-config file, or - for stdin")
		hostname  = fs.String("hostname", "", "local-hostname for meta-data")
		instance  = fs.String("instance-id", "k0smos", "instance-id for meta-data")
		label     = fs.String("label", metadata.NoCloudLabel, "volume label; k0smos looks for "+metadata.NoCloudLabel)
		writeFile = multiFlag{}
	)
	fs.Var(&writeFile, "file", "write a host file onto the node as SRC:DEST (repeatable)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: k0smosctl gen [flags]

Writes a NoCloud cloud-init drive that k0smos reads at boot: user-data and
meta-data at the image root, with Rock Ridge names.

Examples:
  # a config file rendered elsewhere
  k0smosctl gen -user-data cloud-config.yaml -hostname node-1 -o cidata.iso

  # place files on the node without writing cloud-config by hand
  k0smosctl gen -file k0s.yaml:/etc/k0s/k0s.yaml -hostname node-1

  # a manifest k0s will apply on the first reconcile
  k0smosctl gen -file ns.yaml:/var/lib/k0s/manifests/demo/ns.yaml

Then boot with the drive attached:
  CIDATA=cidata.iso make boot

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	body, err := userDataBody(*userData, writeFile)
	if err != nil {
		return err
	}

	var meta strings.Builder
	fmt.Fprintf(&meta, "instance-id: %s\n", *instance)
	if *hostname != "" {
		fmt.Fprintf(&meta, "local-hostname: %s\n", *hostname)
	}

	// Parsed before it is written, so a mistake surfaces here rather than as a
	// warning on a console during a boot that has already happened.
	if _, _, err := metadata.Load(inMemory{"user-data": body, "meta-data": []byte(meta.String())}); err != nil {
		return fmt.Errorf("the generated user-data does not parse: %w", err)
	}

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	err = iso9660.Write(f, *label, []iso9660.File{
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
	fmt.Printf("wrote %s (%d bytes, LABEL=%s)\n", *out, info.Size(), *label)
	return nil
}

// userDataBody returns the cloud-config to place on the drive: either one supplied
// wholesale, or one synthesised from -file arguments.
func userDataBody(path string, files multiFlag) ([]byte, error) {
	switch {
	case path != "" && len(files) > 0:
		return nil, errors.New("-user-data and -file cannot be combined; put the files in the cloud-config")
	case path != "":
		if path == "-" {
			return readAll(os.Stdin)
		}
		return os.ReadFile(path)
	case len(files) > 0:
		return renderWriteFiles(files)
	default:
		return nil, errors.New("nothing to configure: pass -user-data or -file")
	}
}

// renderWriteFiles turns SRC:DEST pairs into a write_files cloud-config.
//
// Content is base64 encoded rather than inlined, so that a file containing
// anything YAML would reinterpret — tabs, colons, leading dashes — survives
// intact. It also means no quoting or indentation rules to get wrong here.
func renderWriteFiles(files multiFlag) ([]byte, error) {
	var b strings.Builder
	b.WriteString("#cloud-config\nwrite_files:\n")
	for _, spec := range files {
		src, dest, ok := strings.Cut(spec, ":")
		if !ok || src == "" || dest == "" {
			return nil, fmt.Errorf("-file %q is not SRC:DEST", spec)
		}
		if !strings.HasPrefix(dest, "/") {
			return nil, fmt.Errorf("-file %q: destination must be an absolute path", spec)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		mode, err := modeOf(src)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  - path: %s\n    permissions: %q\n    encoding: b64\n    content: %s\n",
			dest, mode, base64Of(data))
	}
	return []byte(b.String()), nil
}

func modeOf(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	// Keep the source file's permissions, which is what makes a token or a key
	// land on the node no more readable than it was here.
	return fmt.Sprintf("%04o", info.Mode().Perm()), nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
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

func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func readAll(f *os.File) ([]byte, error) { return io.ReadAll(f) }
