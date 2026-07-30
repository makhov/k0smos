package metadata

import (
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

// Load reads user-data and meta-data from a mounted cloud-init drive. Missing
// files yield empty results rather than errors: a drive may carry only one, and
// an unrelated disk carries neither.
func Load(mountPoint string) (UserData, MetaData, error) {
	ud, err := ParseUserData(readFirst(mountPoint, userDataPaths))
	if err != nil {
		return UserData{}, MetaData{}, err
	}
	md, err := ParseMetaData(readFirst(mountPoint, metaDataPaths))
	if err != nil {
		return ud, MetaData{}, err
	}
	return ud, md, nil
}

func readFirst(root string, names []string) []byte {
	for _, n := range names {
		if b, err := os.ReadFile(filepath.Join(root, n)); err == nil {
			return b
		}
	}
	return nil
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
