package main

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// openPTY returns a new pseudo-terminal's controller and the terminal it drives.
func openPTY() (controller, terminal *os.File, err error) {
	if controller, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0); err != nil {
		return nil, nil, err
	}
	fd := int(controller.Fd())
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err == nil {
		err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0)
	}
	if err == nil {
		terminal, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|unix.O_NOCTTY, 0)
	}
	if err != nil {
		controller.Close()
		return nil, nil, err
	}
	return controller, terminal, nil
}
