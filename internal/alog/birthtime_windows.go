package alog

import (
	"golang.org/x/sys/windows"
	"os"
	"time"
)

func birthtime(f *os.File) (time.Time, error) {
	var stat windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &stat); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, stat.CreationTime.Nanoseconds()).UTC(), nil
}
