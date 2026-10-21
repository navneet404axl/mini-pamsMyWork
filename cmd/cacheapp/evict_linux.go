//go:build linux

package main

import (
	"os"
	"syscall"
)

// evict drops this file's pages from the page cache WITHOUT root.
//
// It calls posix_fadvise(fd, 0, 0, POSIX_FADV_DONTNEED): "I won't need this
// file's data soon." Linux then discards the file's clean cached pages.
// Unlike "echo 3 > /proc/sys/vm/drop_caches" (root only, clears everything),
// this only touches our own data file, so any user may do it.
func evict(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const fadvDontneed = 4 // POSIX_FADV_DONTNEED
	_, _, errno := syscall.Syscall6(syscall.SYS_FADVISE64, f.Fd(), 0, 0, fadvDontneed, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
