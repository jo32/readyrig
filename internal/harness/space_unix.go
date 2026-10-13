//go:build unix

package harness

import "golang.org/x/sys/unix"

// freeBytes reports the space available to this user under dir, or -1 if unknown.
func freeBytes(dir string) int64 {
	var s unix.Statfs_t
	if unix.Statfs(dir, &s) != nil {
		return -1
	}
	return int64(s.Bavail) * int64(s.Bsize)
}
