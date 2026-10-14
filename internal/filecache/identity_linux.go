package filecache

import "syscall"

// The conversions are not redundant everywhere: Stat_t's field types differ
// between linux ports, and this file builds on all of them.
func statIdentity(st *syscall.Stat_t) identity {
	return identity{
		ctime: int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec),
		dev:   uint64(st.Dev), // #nosec G115 -- an identifier compared for equality, never as a number
		ino:   uint64(st.Ino),
	}
}
