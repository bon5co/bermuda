//go:build !linux && !darwin && !windows

package alog

import (
	"fmt"
	"os"
	"time"
)

func birthtime(_ *os.File) (time.Time, error) {
	return time.Time{}, fmt.Errorf("birthtime unavailable on this platform")
}
