package collector

import (
	"bytes"
	"strings"
	"syscall"

	"monitor.uz/internal/model"
)

type mountEntry struct {
	DevID  string // major:minor
	Root   string // root of the mount within the filesystem
	Mount  string
	FSType string
	Source string
}

// parseMountinfo reads /proc/self/mountinfo:
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
func parseMountinfo(b []byte) []mountEntry {
	var out []mountEntry
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		sep := -1
		for i, x := range f {
			if string(x) == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(f) < sep+3 {
			return
		}
		out = append(out, mountEntry{
			DevID:  string(f[2]),
			Root:   unescapeMount(string(f[3])),
			Mount:  unescapeMount(string(f[4])),
			FSType: string(f[sep+1]),
			Source: unescapeMount(string(f[sep+2])),
		})
	})
	return out
}

// unescapeMount decodes the octal escapes (\040 etc.) used in mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			c := (s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0')
			b.WriteByte(c)
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Only local block filesystems are measured. Network filesystems (NFS/CIFS
// storages under /mnt/pve) are skipped on purpose: statvfs on a dead NFS
// server can block the agent.
var localFSTypes = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "vfat": true, "f2fs": true,
}

// selectMounts picks real filesystems: local type, the filesystem's own
// root (not bind mounts), one entry per device, and for ZFS only "/" and
// pool roots (Proxmox mounts every CT subvolume on the host).
func selectMounts(all []mountEntry) []mountEntry {
	seen := map[string]bool{}
	var out []mountEntry
	for _, m := range all {
		if !localFSTypes[m.FSType] || m.Root != "/" || seen[m.DevID] {
			continue
		}
		if m.FSType == "zfs" && m.Mount != "/" && strings.Contains(m.Source, "/") {
			continue
		}
		if strings.HasPrefix(m.Mount, "/run/") || strings.HasPrefix(m.Mount, "/var/lib/lxc/") {
			continue
		}
		seen[m.DevID] = true
		out = append(out, m)
	}
	return out
}

func statFS(m mountEntry) (model.FS, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.Mount, &st); err != nil {
		return model.FS{}, err
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	free := st.Bfree * bs
	avail := st.Bavail * bs
	used := total - free
	// Same formula as df: used / (used + available to non-root).
	return model.FS{
		Mount: m.Mount, Device: m.Source, Type: m.FSType,
		Total: total, Used: used, Avail: avail,
		UsedPct: round2(pct(float64(used), float64(used+avail))),
	}, nil
}
