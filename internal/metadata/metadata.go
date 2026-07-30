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
// write_files and runcmd — not cloud-init as a whole.
package metadata

import (
	"encoding/base64"
	"fmt"
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
	// Warnings records input that was understood but deliberately skipped, so
	// the console shows why something did not happen.
	Warnings []string
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

	for _, f := range cc.WriteFiles {
		content := f.Content
		switch f.Encoding {
		case "", "text/plain":
		case "b64", "base64":
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
			if err != nil {
				out.Warnings = append(out.Warnings,
					fmt.Sprintf("write_files %s: bad base64, skipped", f.Path))
				continue
			}
			content = string(raw)
		default:
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("write_files %s: unsupported encoding %q, skipped", f.Path, f.Encoding))
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
