package sys

import (
	"bufio"
	"bytes"
	"fmt"
)

// parseMountInfo parses /proc/self/mountinfo content. The optional fields
// between the mountpoint and the " - " separator are variable-length, so we
// split on the separator to locate fstype/source reliably.
func parseMountInfo(data []byte) ([]MountPoint, error) {
	var out []MountPoint
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		sep := bytes.Index([]byte(line), []byte(" - "))
		if sep < 0 {
			return nil, fmt.Errorf("mountinfo: no separator in %q", line)
		}
		left := line[:sep]
		right := line[sep+3:]
		lf := splitFields(left)
		rf := splitFields(right)
		if len(lf) < 5 || len(rf) < 2 {
			return nil, fmt.Errorf("mountinfo: short line %q", line)
		}
		out = append(out, MountPoint{Target: lf[4], FSType: rf[0], Source: rf[1]})
	}
	return out, sc.Err()
}

func splitFields(s string) []string {
	var f []string
	for _, tok := range bytes.Fields([]byte(s)) {
		f = append(f, string(tok))
	}
	return f
}
