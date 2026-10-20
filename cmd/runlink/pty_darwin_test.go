package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY returns a new pseudo-terminal's controller and the terminal it drives.
func openPTY() (controller, terminal *os.File, err error) {
	if controller, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0); err != nil {
		return nil, nil, err
	}
	fd := int(controller.Fd())
	name := make([]byte, 128) // the size TIOCPTYGNAME writes
	err = unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0)
	if err == nil {
		err = unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0)
	}
	if err == nil {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
			err = errno
		}
	}
	if err == nil {
		terminal, err = os.OpenFile(string(name[:bytes.IndexByte(name, 0)]), os.O_RDWR|unix.O_NOCTTY, 0)
	}
	if err != nil {
		controller.Close()
		return nil, nil, err
	}
	return controller, terminal, nil
}
