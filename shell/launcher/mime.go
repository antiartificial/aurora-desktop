package launcher

import "github.com/antiartificial/aurora/contracts/plumb"

// DetectMIME is contracts/plumb's: one table and one sniffing rule, so
// `aurora open` and the plumber agree on what a file is.
func DetectMIME(path string) (string, error) { return plumb.DetectMIME(path) }
