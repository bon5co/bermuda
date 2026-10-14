//go:build linux || darwin

package filecache

import (
	"io/fs"
	"syscall"
)

// identityOf reads what size and mtime cannot vouch for from the stat the
// caller already made: the inode change time, which the kernel sets on every
// write, chmod and utime and which nothing in user space can set back, and the
// device and inode, which tell a file from one renamed over it.
func identityOf(info fs.FileInfo) identity {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return identity{}
	}
	return statIdentity(st)
}
