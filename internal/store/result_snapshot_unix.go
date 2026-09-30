//go:build unix

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

func openResultSnapshot(path string) (*os.File, error) {
	// A replacement FIFO must not block Open between the path and fd checks.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
