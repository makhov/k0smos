package sys

import "testing"

// A real root hash, from image/mkmetal.sh building the tree for dist/k0smos.erofs.
const testRootHash = "bda8e1d1488ecda35455eab6ca6aa43ce3c95f642fbdca5c35e42708b752323e"

func TestParseVeritySpec(t *testing.T) {
	blocks, hash, err := parseVeritySpec("46918:" + testRootHash)
	if err != nil {
		t.Fatal(err)
	}
	if blocks != 46918 || hash != testRootHash {
		t.Errorf("parseVeritySpec = %d, %q", blocks, hash)
	}

	// Every malformed value must be an error, never a zero value: a caller that
	// treated one as "no verity" would mount the root unverified, which is the
	// single outcome this argument exists to prevent.
	for _, spec := range []string{
		"", "46918", testRootHash, "46918:", "0:" + testRootHash,
		"-1:" + testRootHash, "4x:" + testRootHash,
		"46918:" + testRootHash[:63],      // too short
		"46918:" + testRootHash + "ab",    // too long
		"46918:" + "z" + testRootHash[1:], // not hex
	} {
		if _, _, err := parseVeritySpec(spec); err == nil {
			t.Errorf("parseVeritySpec(%q) = nil error, want one", spec)
		}
	}
}

func TestVerityTable(t *testing.T) {
	// The hash tree follows the data on the same device, so data device and hash
	// device are the same and the tree starts at the first block past the data.
	got := verityTable("/dev/sda2", 46918, testRootHash)
	want := "1 /dev/sda2 /dev/sda2 4096 4096 46918 46918 sha256 " + testRootHash + " -"
	if got != want {
		t.Errorf("verityTable =\n  %s\nwant\n  %s", got, want)
	}
}

func TestDecodeDev(t *testing.T) {
	// device-mapper reports new_encode_dev, which splits the minor so it can hold
	// 20 bits. Reading it as major<<8|minor happens to work for minor 0 and 1 --
	// the devices a test on one machine would see -- and gives the wrong node for
	// anything above 255, which is why a case past the split is here.
	for _, c := range []struct {
		enc          uint64
		major, minor uint32
	}{
		{0xfd00, 253, 0},
		{0xfd01, 253, 1},
		{0x10fd00, 253, 256},
	} {
		major, minor := decodeDev(c.enc)
		if major != c.major || minor != c.minor {
			t.Errorf("decodeDev(%#x) = %d:%d, want %d:%d", c.enc, major, minor, c.major, c.minor)
		}
	}
}
