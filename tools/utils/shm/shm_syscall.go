// License: GPLv3 Copyright: 2022, Kovid Goyal, <kovid at kovidgoyal.net>
//go:build darwin || freebsd

package shm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

var _ = fmt.Print

// ByteSliceFromString makes a zero terminated byte slice from the string
func ByteSliceFromString(s string) []byte {
	a := make([]byte, len(s)+1)
	copy(a, s)
	return a
}

func BytePtrFromString(s string) *byte {
	a := ByteSliceFromString(s)
	return &a[0]
}

func shm_unlink(name string) (err error) {
	bname := BytePtrFromString(name)
	for {
		_, _, errno := unix.Syscall(unix.SYS_SHM_OPEN, uintptr(unsafe.Pointer(bname)), 0, 0)
		if errno != unix.EINTR {
			if errno != 0 {
				err = fmt.Errorf("shm_unlink() failed with error: %w", errno)
			}
			break
		}
	}
	return
}

func shm_open(name string, flags, perm int) (ans *os.File, err error) {
	bname := BytePtrFromString(name)
	var fd uintptr
	var errno unix.Errno
	for {
		fd, _, errno = unix.Syscall(unix.SYS_SHM_OPEN, uintptr(unsafe.Pointer(bname)), uintptr(flags), uintptr(perm))
		if errno != unix.EINTR {
			if errno != 0 {
				err = fmt.Errorf("shm_open() failed with error: %w", errno)
			}
			break
		}
	}
	if err == nil {
		ans = os.NewFile(fd, name)
	}
	return
}

type syscall_based_mmap struct {
	f        *os.File
	region   []byte
	unlinked bool
}

func syscall_mmap(f *os.File, size uint64, access ProtectionFlags, truncate bool) (MMap, error) {
	if truncate {
		err := truncate_or_unlink(f, size)
		if err != nil {
			return nil, fmt.Errorf("truncate failed with error: %w", err)
		}
	}
	region, err := mmap(int(size), access, false, int(f.Fd()), 0)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("mmap failed with error: %w", err)
	}
	return &syscall_based_mmap{f: f, region: region}, nil
}

func (self *syscall_based_mmap) Name() string {
	return self.f.Name()
}

func (self *syscall_based_mmap) Slice() []byte {
	return self.region
}

func (self *syscall_based_mmap) Close() error {
	err := self.f.Close()
	self.region = nil
	return err
}

func (self *syscall_based_mmap) Unlink() (err error) {
	if self.unlinked {
		return nil
	}
	self.unlinked = true
	return shm_unlink(self.Name())
}

func create_temp(pattern string, size uint64) (ans MMap, err error) {
	var prefix, suffix string
	prefix, suffix, err = prefix_and_suffix(pattern)
	if err != nil {
		return
	}
	if SHM_REQUIRED_PREFIX != "" && !strings.HasPrefix(pattern, SHM_REQUIRED_PREFIX) {
		// FreeBSD requires name to start with /
		prefix = SHM_REQUIRED_PREFIX + prefix
	}
	var f *os.File
	try := 0
	for {
		name := prefix + next_random() + suffix
		if len(name) > SHM_NAME_MAX {
			return nil, ErrPatternTooLong
		}
		f, err = shm_open(name, os.O_EXCL|os.O_CREATE|os.O_RDWR, 0600)
		if err != nil && (errors.Is(err, fs.ErrExist) || errors.Unwrap(err) == unix.EEXIST) {
			try += 1
			if try > 10000 {
				return nil, &os.PathError{Op: "createtemp", Path: prefix + "*" + suffix, Err: ErrExist}
			}
			continue
		}
		break
	}
	if err != nil {
		return nil, err
	}
	return syscall_mmap(f, size, RDWR, true)
}

func Open(name string) (MMap, error) {
	ans, err := shm_open(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	s, err := os.Stat(name)
	if err != nil {
		ans.Close()
		return nil, err
	}
	return syscall_mmap(ans, uint64(s.Size()), READ, false)
}
