//go:build linux || darwin

package ttyio

import "golang.org/x/sys/unix"

// CharMode puts the terminal on fd into character mode, for the REPL's
// box: no line buffering, no echo, no signals — Ctrl-C, Ctrl-Z, Ctrl-S and
// Ctrl-Q reach the box as keys — and Enter comes as \r, so Ctrl-J (\n) is
// a key of its own. Output is left as it was. restore puts the old mode back.
func CharMode(fd int) (restore func(), err error) {
	return setMode(fd, func(t *unix.Termios) {
		t.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
		t.Iflag &^= unix.ICRNL | unix.INLCR | unix.IXON
		t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	})
}

// NoEcho turns the echo off on fd — a secret is typed as a line, unseen.
// restore puts the old mode back.
func NoEcho(fd int) (restore func(), err error) {
	return setMode(fd, func(t *unix.Termios) { t.Lflag &^= unix.ECHO })
}

func setMode(fd int, change func(*unix.Termios)) (restore func(), err error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	change(&t)
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

// Width is the width in columns of the terminal on fd, 0 when unknown.
func Width(fd int) int {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}
