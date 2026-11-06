package ticket

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// KeyPath is where the controller and the picker share the signing key:
// $AURORA_POWERBOX_KEY, else $XDG_RUNTIME_DIR/aurora/powerbox.key (the same
// directory convention as the bus socket).
func KeyPath() string { return keyPath("AURORA_POWERBOX_KEY", "powerbox.key") }

// ApprovalKeyPath is where the controller and the compositor share the key the
// compositor signs its approval replies with: $AURORA_APPROVAL_KEY, else
// $XDG_RUNTIME_DIR/aurora/approval.key. A different file from the powerbox
// key, loaded and created by the same LoadKey (compositor/src/trusted/sign.rs
// computes the same path).
func ApprovalKeyPath() string { return keyPath("AURORA_APPROVAL_KEY", "approval.key") }

func keyPath(env, name string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = filepath.Join(os.TempDir(), fmt.Sprintf("aurora-%d", os.Getuid()))
	}
	return filepath.Join(rt, "aurora", name)
}

// LoadKey reads the 32-byte key. It refuses a file that is not a regular
// file owned by this user with mode 0600 (a looser file could have been read
// or replaced by someone else). With create, a missing file is made, atomically
// and 0600, with random bytes.
func LoadKey(path string, create bool) ([]byte, error) {
	k, err := readKey(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".powerbox-key-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(b); err != nil {
			tmp.Close()
			return nil, err
		}
		tmp.Close() // CreateTemp makes it 0600
		if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		return readKey(path)
	}
	return k, err
}

func readKey(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	sys, _ := st.Sys().(*syscall.Stat_t)
	switch {
	case !st.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", path)
	case st.Mode().Perm() != 0o600:
		return nil, fmt.Errorf("%s has mode %o; want 0600", path, st.Mode().Perm())
	case sys == nil || int(sys.Uid) != os.Getuid():
		return nil, fmt.Errorf("%s is not owned by this user", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s is %d bytes; want 32", path, len(b))
	}
	return b, nil
}
