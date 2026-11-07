package dom

import (
	"os"
	"testing"
)

// The test binary is what a domain with a net grant binds in as its relay
// (os.Executable), so it must answer __netfwd the way the domain command does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == NetFwdCmd {
		os.Exit(RunNetFwd(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == DropCmd { // nsenter re-runs this binary as the drop-mount helper
		os.Exit(RunDropHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}
