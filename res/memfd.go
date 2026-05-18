//go:build linux

package res

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func MemfdFromBytes(name string, b []byte) (int, error) {
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("memfd_create: %w", err)
	}
	for off := 0; off < len(b); {
		n, err := unix.Write(fd, b[off:])
		if err != nil {
			unix.Close(fd)
			return -1, fmt.Errorf("write: %w", err)
		}
		off += n
	}
	if _, err := unix.Seek(fd, 0, unix.SEEK_SET); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("seek: %w", err)
	}
	return fd, nil
}
