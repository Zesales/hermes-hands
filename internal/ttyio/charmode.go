//go:build linux || darwin

package ttyio

import "golang.org/x/sys/unix"

// CharMode puts the terminal on fd into character mode, for reading what the
// operator types while a turn runs: no line buffering, no echo — signals
// stay on, so Ctrl-C still cancels the turn. restore puts the old mode back.
func CharMode(fd int) (restore func(), err error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ICANON | unix.ECHO
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}

// Readable waits up to ms milliseconds for input on fd.
func Readable(fd, ms int) bool {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, ms)
	return err == nil && n > 0 && fds[0].Revents&unix.POLLIN != 0
}
