package collector

import (
	"os"
	"strconv"
	"syscall"
)

func statIno(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return ":" + strconv.FormatUint(st.Ino, 10)
	}
	return ""
}
