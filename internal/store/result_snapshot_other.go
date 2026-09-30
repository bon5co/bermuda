//go:build !unix

package store

import "os"

func openResultSnapshot(path string) (*os.File, error) {
	return os.Open(path)
}
