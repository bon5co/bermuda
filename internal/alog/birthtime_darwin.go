package alog

import (
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func birthtime(f *os.File) (time.Time, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return time.Time{}, err
	}
	return time.Unix(stat.Btim.Sec, stat.Btim.Nsec).UTC(), nil
}
