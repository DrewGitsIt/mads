package goosecli

import "golang.org/x/sys/unix"

// termios ioctl requests for fakegoose's raw-mode switch.
const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)
