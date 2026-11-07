package picker

import (
	"os"
	"os/exec"
)

// Env is the part of the process environment the route and the attestation
// look at, so tests do not have to touch the real one.
type Env struct {
	Getenv   func(string) string
	Self     string // path of the running executable, "" if unknown
	LookPath func(string) (string, error)
	IsSocket func(string) bool
}

// OSEnv is the real environment.
func OSEnv() Env {
	self, _ := os.Executable()
	return Env{
		Getenv:   os.Getenv,
		Self:     self,
		LookPath: exec.LookPath,
		IsSocket: func(p string) bool {
			st, err := os.Stat(p)
			return err == nil && st.Mode()&os.ModeSocket != 0
		},
	}
}
