//go:build !windows

package secret

import (
	"errors"
	"os"
)

// openTTY returns the controlling terminal rather than the standard streams.
//
// The askpass helper is started by ssh with its standard output wired to a
// pipe, and it may have no usable standard input at all. Writing the prompt to
// that pipe would hand the question to ssh as if it were the answer. Opening
// /dev/tty gets the terminal back regardless of how the streams were wired.
func openTTY() (in *os.File, out *os.File, err error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, errors.New("no terminal to ask on")
	}
	return f, f, nil
}
