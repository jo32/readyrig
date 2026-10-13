//go:build !unix

package harness

// freeBytes reports the space available under dir, or -1 if unknown.
func freeBytes(string) int64 { return -1 }
