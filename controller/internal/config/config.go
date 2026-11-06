// Package config holds defaults shared by controllerd and the controller CLI.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// BusSocket returns the bus socket path the same way busd does.
func BusSocket() string {
	if s := os.Getenv("AURORA_BUS_SOCK"); s != "" {
		return s
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = filepath.Join(os.TempDir(), fmt.Sprintf("aurora-%d", os.Getuid()))
	}
	return filepath.Join(rt, "aurora", "bus.sock")
}

// Service is the bus service name the registry registers.
const Service = "registry"
