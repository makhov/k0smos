// Package metadata reads machine configuration from a cloud-init drive.
//
// This is how Cluster API hands a machine its identity. A CAPI bootstrap
// provider (kubeadm, or k0smotron for k0s) renders a cloud-config document into
// a Secret; the infrastructure provider attaches it to the machine as a NoCloud
// ISO labelled "cidata" or an OpenStack config-drive labelled "config-2". Without
// reading it, a machine cannot know whether it is a control plane or a worker,
// and has no join token.
//
// Only the subset that bootstrap providers actually emit is supported —
// write_files and runcmd — plus a small k0smos network section used to
// specialise identical immutable artifacts. This is not cloud-init as a whole.
package metadata

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// UserData is the parsed cloud-config.
type UserData struct {
	WriteFiles []WriteFile
	// RunCmd holds commands as argv slices, already normalised from
	// cloud-init's two accepted forms.
	RunCmd [][]string
	// Machine carries the small part of machine configuration that cannot be
	// baked into an immutable platform artifact. In particular, every member of
	// a local cluster boots the same qcow2 but needs its own address on the shared
	// segment before k0s starts.
	Machine MachineConfig
	// Warnings records input that was understood but deliberately skipped, so
	// the console shows why something did not happen.
	Warnings []string
}

// MachineConfig is k0smos's cloud-config extension. It deliberately mirrors
// the networking names used on the kernel command line; cloud-config values win
// when present, allowing one immutable image to be specialised per machine by
// its ordinary metadata drive.
type MachineConfig struct {
	IP      string
	Iface   string
	Gateway string
	DNS     string
}

// WriteFile is one entry of cloud-init's write_files.
type WriteFile struct {
	Path        string
	Content     string
	Permissions string
}

// Mode parses the octal permissions, defaulting to 0644 as cloud-init does.
func (w WriteFile) Mode() fs.FileMode {
	if w.Permissions == "" {
		return 0644
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(w.Permissions, "0o"), 8, 32)
	if err != nil {
		return 0644
	}
	return fs.FileMode(n)
}

// cloudConfig mirrors the YAML. runcmd entries are left untyped because
// cloud-init permits either a string or a list per entry.
type cloudConfig struct {
	WriteFiles []struct {
		Path        string `json:"path"`
		Content     string `json:"content"`
		Encoding    string `json:"encoding"`
		Permissions string `json:"permissions"`
	} `json:"write_files"`
	RunCmd []any `json:"runcmd"`
	K0smos struct {
		IP      string `json:"ip"`
		Iface   string `json:"iface"`
		Gateway string `json:"gateway"`
		DNS     string `json:"dns"`
	} `json:"k0smos"`
}

// decodeContent applies cloud-init's write_files encoding.
//
// The gzip pairings matter for Kubernetes manifests: k0s applies anything left
// in /var/lib/k0s/manifests/<stack>/, so shipping addons as files is the way to
// deploy without a shell — and manifests are large enough that providers
// compress them.
//
// Bare "gz"/"gzip" is not supported: content arrives as a JSON string, because
// sigs.k8s.io/yaml converts YAML to JSON, and raw compressed bytes cannot travel
// in one. That is exactly why the base64 pairing exists.
func decodeContent(encoding, content string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "text/plain":
		return content, nil

	case "b64", "base64":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			return "", errors.New("bad base64")
		}
		return string(raw), nil

	case "gz+base64", "gzip+base64", "gz+b64", "gzip+b64":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			return "", errors.New("bad base64")
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", fmt.Errorf("bad gzip: %w", err)
		}
		defer zr.Close()
		out, err := io.ReadAll(zr)
		if err != nil {
			return "", fmt.Errorf("bad gzip: %w", err)
		}
		return string(out), nil
	}
	return "", fmt.Errorf("unsupported encoding %q", encoding)
}

// shellMeta are characters that only mean something to a shell. k0smos has no
// shell, so a command containing them cannot be run faithfully.
const shellMeta = "|&;<>$`*?()[]{}!~"

// ParseUserData parses a cloud-config document. Input that is not a
// cloud-config (empty, a shebang script, plain text) yields an empty result
// rather than an error: not every platform supplies one.
func ParseUserData(b []byte) (UserData, error) {
	var out UserData
	if len(strings.TrimSpace(string(b))) == 0 {
		return out, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "#cloud-config") {
		out.Warnings = append(out.Warnings, "user-data is not a #cloud-config document; ignoring")
		return out, nil
	}

	var cc cloudConfig
	if err := yaml.Unmarshal(b, &cc); err != nil {
		return out, fmt.Errorf("parse cloud-config: %w", err)
	}
	out.Machine = MachineConfig{
		IP: cc.K0smos.IP, Iface: cc.K0smos.Iface,
		Gateway: cc.K0smos.Gateway, DNS: cc.K0smos.DNS,
	}

	for _, f := range cc.WriteFiles {
		content, err := decodeContent(f.Encoding, f.Content)
		if err != nil {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("write_files %s: %v, skipped", f.Path, err))
			continue
		}
		out.WriteFiles = append(out.WriteFiles, WriteFile{
			Path: f.Path, Content: content, Permissions: f.Permissions,
		})
	}

	for _, entry := range cc.RunCmd {
		switch v := entry.(type) {
		case string:
			// A bare string is meant for a shell. Without one, only a plain
			// command can be honoured; anything with shell syntax is skipped
			// rather than mis-executed.
			if strings.ContainsAny(v, shellMeta) {
				out.Warnings = append(out.Warnings,
					fmt.Sprintf("runcmd %q needs a shell, skipped", v))
				continue
			}
			if argv := strings.Fields(v); len(argv) > 0 {
				out.RunCmd = append(out.RunCmd, argv)
			}
		case []any:
			argv := make([]string, 0, len(v))
			for _, a := range v {
				s, ok := a.(string)
				if !ok {
					s = fmt.Sprint(a)
				}
				argv = append(argv, s)
			}
			if len(argv) > 0 {
				out.RunCmd = append(out.RunCmd, argv)
			}
		default:
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("runcmd entry of unexpected type %T, skipped", entry))
		}
	}
	return out, nil
}

// MetaData is the subset of instance metadata k0smos uses.
type MetaData struct {
	InstanceID string
	Hostname   string
}

// ParseMetaData reads NoCloud meta-data (YAML) or an OpenStack meta_data.json.
// The key names differ between them, so both spellings are accepted.
func ParseMetaData(b []byte) (MetaData, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return MetaData{}, nil
	}
	var raw struct {
		InstanceID    string `json:"instance-id"`
		LocalHostname string `json:"local-hostname"`
		Hostname      string `json:"hostname"`
		UUID          string `json:"uuid"`
		Name          string `json:"name"`
	}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return MetaData{}, fmt.Errorf("parse meta-data: %w", err)
	}
	md := MetaData{InstanceID: raw.InstanceID, Hostname: raw.LocalHostname}
	if md.InstanceID == "" {
		md.InstanceID = raw.UUID
	}
	if md.Hostname == "" {
		md.Hostname = raw.Hostname
	}
	if md.Hostname == "" {
		md.Hostname = raw.Name
	}
	return md, nil
}
