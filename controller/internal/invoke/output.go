package invoke

import (
	"os"
	"syscall"
)

// prepareOutput makes sure the single output file exists and is empty, so the
// domain can be granted exactly that file (a bind mount needs an existing
// path) and the containing directory stays out of the sandbox. It reports
// whether it created the file, so a failed invoke can remove it again. The
// open refuses to follow a symlink in the last component. The app must write
// the file in place: a rename over a bind-mounted file fails inside the
// domain (see README).
func prepareOutput(path string) (created bool, e *Error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err == nil {
		f.Close()
		return true, nil
	}
	if !os.IsExist(err) {
		return false, fail("bad_output", "cannot create output file", err.Error())
	}
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, fail("bad_output", "cannot open existing output file for writing", err.Error())
	}
	f.Close()
	return false, nil
}
