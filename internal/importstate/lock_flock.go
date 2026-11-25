//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package importstate

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockJournal(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch err {
		case unix.EINTR:
			continue
		case unix.EWOULDBLOCK:
			return ErrJournalInUse
		default:
			return err
		}
	}
}
