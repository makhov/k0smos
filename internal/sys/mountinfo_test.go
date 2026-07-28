package sys

import "testing"

func TestParseMountInfo(t *testing.T) {
	// fields: id parent maj:min root mountpoint opts... - fstype source super
	data := []byte(
		"22 28 0:21 / /proc rw,nosuid - proc proc rw\n" +
			"24 28 0:6 / /dev rw - devtmpfs devtmpfs rw\n")
	mps, err := parseMountInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(mps) != 2 {
		t.Fatalf("want 2 mounts, got %d", len(mps))
	}
	if mps[0].Target != "/proc" || mps[0].FSType != "proc" {
		t.Errorf("mount0 = %+v", mps[0])
	}
	if mps[1].Target != "/dev" || mps[1].FSType != "devtmpfs" {
		t.Errorf("mount1 = %+v", mps[1])
	}
}
