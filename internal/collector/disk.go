package collector

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// diskCounters are the cumulative fields of one /proc/diskstats line we use.
// See Documentation/admin-guide/iostats.rst in the kernel tree.
type diskCounters struct {
	Reads, ReadSectors, ReadMs    uint64
	Writes, WriteSectors, WriteMs uint64
	IOTicks                       uint64 // ms the device had I/O in flight
}

// sectorSize: /proc/diskstats always counts 512-byte sectors.
const sectorSize = 512

func parseDiskstats(b []byte) (map[string]diskCounters, error) {
	out := map[string]diskCounters{}
	var perr error
	lines(b, func(l []byte) {
		if perr != nil {
			return
		}
		f := bytes.Fields(l)
		if len(f) == 0 {
			return
		}
		if len(f) < 14 {
			perr = fmt.Errorf("diskstats: short line %q", l)
			return
		}
		get := func(i int) uint64 {
			v, err := parseUint(f[i])
			if err != nil && perr == nil {
				perr = fmt.Errorf("diskstats %s: %w", f[2], err)
			}
			return v
		}
		out[string(f[2])] = diskCounters{
			Reads: get(3), ReadSectors: get(5), ReadMs: get(6),
			Writes: get(7), WriteSectors: get(9), WriteMs: get(10),
			IOTicks: get(12),
		}
	})
	return out, perr
}

// wholeDisk reports whether name is a whole physical (or controller logical)
// disk, not a partition, LVM/dm volume, zvol or loop device. Partitions
// have no "device" link; md arrays are kept explicitly.
func wholeDisk(sysRoot, name string) bool {
	if strings.HasPrefix(name, "md") {
		_, err := os.Stat(filepath.Join(sysRoot, "block", name))
		return err == nil
	}
	// cciss-style names use '!' in sysfs instead of '/'.
	sysName := strings.ReplaceAll(name, "/", "!")
	_, err := os.Stat(filepath.Join(sysRoot, "block", sysName, "device"))
	return err == nil
}
