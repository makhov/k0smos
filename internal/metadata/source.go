package metadata

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Drive labels used by the two formats CAPI infrastructure providers attach.
// KubeVirt's cloudInitNoCloud produces the first; OpenStack-style config-drives
// (including Ironic's, for bare metal) produce the second.
const (
	NoCloudLabel     = "cidata"
	ConfigDriveLabel = "config-2"
)

// Labels is the search order for a cloud-init drive.
var Labels = []string{NoCloudLabel, ConfigDriveLabel}

// candidate file locations, tried in order: NoCloud at the root, then the
// OpenStack config-drive layout.
var (
	userDataPaths = []string{"user-data", "openstack/latest/user_data"}
	metaDataPaths = []string{"meta-data", "openstack/latest/meta_data.json"}
)

// Files supplies the contents of a named file from a cloud-init drive, with
// forward-slash paths relative to its root.
//
// This is an interface because the drive is not always mounted: an ISO is parsed
// in userspace by internal/iso9660, which satisfies this directly and so needs
// no kernel filesystem support. Dir covers the mounted case.
type Files interface {
	ReadFile(name string) ([]byte, error)
}

// Dir reads from a mounted drive.
type Dir string

func (d Dir) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), filepath.FromSlash(name)))
}

// Load reads user-data and meta-data from a cloud-init drive. Missing files
// yield empty results rather than errors: a drive may carry only one, and an
// unrelated disk carries neither.
func Load(f Files) (UserData, MetaData, error) {
	userData, userWarn := readFirst(f, userDataPaths)
	metaData, metaWarn := readFirst(f, metaDataPaths)

	ud, err := ParseUserData(userData)
	if err != nil {
		return UserData{}, MetaData{}, err
	}
	// A drive that cannot be read is not the same as a drive carrying nothing,
	// and the difference matters: a machine whose bootstrap data was silently
	// dropped comes up configured as if none was supplied and never joins. Both
	// still boot, but only one of them should look like a normal boot.
	ud.Warnings = append(ud.Warnings, append(userWarn, metaWarn...)...)

	md, err := ParseMetaData(metaData)
	if err != nil {
		return ud, MetaData{}, err
	}
	return ud, md, nil
}

// readFirst returns the first of names that exists, plus a warning for each
// candidate that existed but could not be read. A file simply not being present
// is normal — a drive carries one layout, not both — and is not warned about.
func readFirst(f Files, names []string) ([]byte, []string) {
	var warnings []string
	for _, n := range names {
		b, err := f.ReadFile(n)
		if err == nil {
			return b, warnings
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// The error already names the file, from either the ISO reader or
			// os.ReadFile, so this does not repeat it.
			warnings = append(warnings, fmt.Sprintf("could not read %v", err))
		}
	}
	return nil, warnings
}

// Apply writes the files described by user-data. mkdirAll and writeFile are
// injected so this is testable and so the caller can route them through
// internal/sys.
func Apply(u UserData, mkdirAll func(string, fs.FileMode) error, writeFile func(string, []byte, fs.FileMode) error) error {
	for _, f := range u.WriteFiles {
		if f.Path == "" {
			continue
		}
		// Bootstrap data routinely targets directories the image does not ship.
		if err := mkdirAll(filepath.Dir(f.Path), 0755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Path, err)
		}
		if err := writeFile(f.Path, []byte(f.Content), f.Mode()); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	return nil
}
