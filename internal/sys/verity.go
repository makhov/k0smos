package sys

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// verityName is the device-mapper name of the verified root, and so the node
// under /dev/mapper that VerityOpen returns.
const verityName = "k0smos-root"

// verityBlockSize is the block size the hash tree was built with (see
// image/mkmetal.sh). dm-verity cannot discover it -- the table has to say -- and
// a wrong value fails on the first block read rather than at setup.
const verityBlockSize = 4096

// parseVeritySpec reads a k0smos.verity= value: <data-blocks>:<root-hash>, as
// image/mkmetal.sh writes it into grub.cfg.
//
// Everything is rejected rather than ignored. A malformed value must not fall
// back to mounting the root unverified, which is what the argument exists to
// prevent.
func parseVeritySpec(spec string) (blocks uint64, hash string, err error) {
	count, hash, ok := strings.Cut(spec, ":")
	if !ok {
		return 0, "", fmt.Errorf("want <data-blocks>:<root-hash>, got %q", spec)
	}
	blocks, err = strconv.ParseUint(count, 10, 64)
	if err != nil || blocks == 0 {
		return 0, "", fmt.Errorf("data block count %q is not a positive number", count)
	}
	if _, err := hex.DecodeString(hash); err != nil || len(hash) != 2*sha256.Size {
		return 0, "", fmt.Errorf("root hash %q is not %d hex digits", hash, 2*sha256.Size)
	}
	return blocks, hash, nil
}

// verityTable is the dm-verity target table for a root whose hash tree sits
// immediately after its data on the same device, which is how mkmetal.sh lays
// out the ROOT partition -- so the data device and the hash device are one, and
// the tree starts at the block the data ends on.
//
// The fields, in order: table version, data device, hash device, data block
// size, hash block size, data blocks, the block the tree starts at, digest
// algorithm, root hash, salt. "-" is the empty salt, which is what makes the
// tree reproducible from the image alone.
func verityTable(dev string, blocks uint64, hash string) string {
	return fmt.Sprintf("1 %s %s %d %d %d %d sha256 %s -",
		dev, dev, verityBlockSize, verityBlockSize, blocks, blocks, hash)
}

// decodeDev splits the device number device-mapper reports into major and minor.
// Its encoding is the kernel's new_encode_dev, which is not major<<8|minor: the
// minor is split in two so it can hold 20 bits.
func decodeDev(dev uint64) (major, minor uint32) {
	return uint32((dev & 0xfff00) >> 8), uint32((dev & 0xff) | ((dev >> 12) & 0xfff00))
}
