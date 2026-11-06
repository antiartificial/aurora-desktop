// Command aurora is the launcher CLI: pick an object, pick a verb, run it.
package main

import (
	"bufio"
	"os"

	"github.com/antiartificial/aurora/shell/launcher"
)

func main() {
	env := &launcher.Env{
		Backend: launcher.DialBackend,
		In:      bufio.NewReader(os.Stdin),
		Out:     os.Stdout,
		Err:     os.Stderr,
		Tty:     launcher.OpenTTY,
	}
	os.Exit(launcher.Run(env, os.Args[1:]))
}
