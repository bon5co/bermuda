package filecache

import "syscall"

func statIdentity(st *syscall.Stat_t) identity {
	return identity{
		ctime: st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec,
		dev:   uint64(st.Dev), // #nosec G115 -- an identifier compared for equality, never as a number
		ino:   st.Ino,
	}
}
