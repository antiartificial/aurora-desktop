// Command lumen is the Lumen image app: resize, export and histogram.
package main

import (
	"os"

	"github.com/antiartificial/aurora/apps/lumen"
)

func main() {
	os.Exit(lumen.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
