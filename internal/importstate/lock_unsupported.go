//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package importstate

import (
	"errors"
	"os"
)

func lockJournal(_ *os.File) error {
	return errors.ErrUnsupported
}
