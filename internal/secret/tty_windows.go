//go:build windows

package secret

import (
	"errors"
	"os"
)

// openTTY returns handles on the console itself rather than on the standard
// streams.
//
// The askpass helper is started by ssh with its standard output wired to a
// pipe, and it may have no usable standard input at all. Writing the prompt to
// that pipe would hand the question to ssh as if it were the answer. So the
// console is opened directly, by name, which works even when every standard
// stream has been redirected.
func openTTY() (in *os.File, out *os.File, err error) {
	in, err = os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, errors.New("no console to ask on")
	}
	out, err = os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		in.Close()
		return nil, nil, errors.New("no console to ask on")
	}
	return in, out, nil
}
