//go:build !linux && !darwin

package filecache

import "io/fs"

// identityOf has nothing beyond size and mtime to offer where the stat does not
// carry an inode change time; those platforms keep the size-and-mtime check.
func identityOf(fs.FileInfo) identity { return identity{} }
