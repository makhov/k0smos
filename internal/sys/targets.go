package sys

// targetsOf reduces mount points to just their targets.
func targetsOf(mps []MountPoint) []string {
	out := make([]string, 0, len(mps))
	for _, mp := range mps {
		out = append(out, mp.Target)
	}
	return out
}
