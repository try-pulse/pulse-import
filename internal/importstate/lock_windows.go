package importstate

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockJournal(file *os.File) error {
	// A reserved byte beyond the journal contents allows read-only inspection
	// from another handle while serializing all importer and rollback writers.
	region := windows.Overlapped{OffsetHigh: 1 << 30}
	err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &region)
	if err == windows.ERROR_LOCK_VIOLATION {
		return ErrJournalInUse
	}
	return err
}
