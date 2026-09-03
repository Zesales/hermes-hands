// Package ttyio holds two small terminal primitives: an is-a-tty check and
// opening the controlling /dev/tty. Pure stdlib, no x/term.
package ttyio

import "os"

// IsTerminal reports whether f is attached to a character device, i.e. the Go
// equivalent of bash `[[ -t FD ]]`.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// OpenControllingTTY opens /dev/tty read/write, mirroring hh_confirm's
// `>/dev/tty` / `</dev/tty`. It returns an error when the process has no
// controlling terminal (or the platform has no /dev/tty), which callers treat
// as "no tty for approval".
func OpenControllingTTY() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}
