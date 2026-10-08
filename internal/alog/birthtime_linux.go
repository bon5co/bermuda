package alog

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func birthtime(f *os.File) (time.Time, error) {
	var stat unix.Statx_t
	if err := unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_BTIME, &stat); err != nil {
		return time.Time{}, err
	}
	if stat.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, fmt.Errorf("filesystem does not report birthtime")
	}
	return time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)).UTC(), nil
}
