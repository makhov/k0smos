package sys

// MountPoint is one entry from /proc/self/mountinfo.
type MountPoint struct {
	Source string
	Target string
	FSType string
}
