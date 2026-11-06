package dom

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Device grants (decision 0014). A domain's /dev is a minimal tmpfs (bwrap
// --dev), so a device node is absent until a grant puts it there. Two classes:
//
//	gpu      every /dev/dri/renderD* node. The RENDER node, never the card node:
//	         renderD* is unprivileged compute and render with no modesetting and
//	         no display-master rights; card* is the KMS node that can drive the
//	         screen and take DRM master, which a domain must not hold.
//	wayland  the compositor's Wayland socket, bound alone to DisplayPath. Never
//	         its parent directory: $XDG_RUNTIME_DIR/aurora holds approval.key and
//	         powerbox.key, and the parent of the socket may hold more.
var DeviceClasses = []string{"gpu", "wayland"}

// DisplayPath is where a wayland grant puts the socket inside the domain. It
// is an absolute WAYLAND_DISPLAY, so clients need no XDG_RUNTIME_DIR.
const DisplayPath = "/run/aurora-display/wayland-0"

func KnownDevice(c string) bool {
	for _, k := range DeviceClasses {
		if k == c {
			return true
		}
	}
	return false
}

func DeviceClassList() string { return strings.Join(DeviceClasses, ", ") }

func hasDevice(m *Meta, class string) bool {
	for _, g := range m.Grants {
		if g.Kind == "device" && g.Device == class {
			return true
		}
	}
	return false
}

// RenderNodes lists the host render nodes (sorted); never card nodes.
func RenderNodes() []string {
	n, _ := filepath.Glob("/dev/dri/renderD*")
	sort.Strings(n)
	return n
}

// WaylandSocket is the host path of the compositor's socket: AURORA_WAYLAND,
// else the name in $XDG_RUNTIME_DIR/aurora/display (written by aurora-up once
// the compositor has chosen its socket), joined to XDG_RUNTIME_DIR if relative.
// WAYLAND_DISPLAY is deliberately NOT consulted: in a user's shell it names the
// HOST's compositor, and falling back to it would hand a domain the one
// compositor Aurora is not. The result must be a unix socket, so a
// misconfigured name can never turn this bind into a file or directory grant.
func WaylandSocket() (string, error) {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	n := os.Getenv("AURORA_WAYLAND")
	if n == "" && rt != "" {
		if b, err := os.ReadFile(filepath.Join(rt, "aurora", "display")); err == nil {
			n = strings.TrimSpace(string(b))
		}
	}
	if n == "" {
		return "", fmt.Errorf("no Aurora compositor socket known: AURORA_WAYLAND is unset and %s/aurora/display does not exist", rt)
	}
	if !filepath.IsAbs(n) {
		if rt == "" {
			return "", fmt.Errorf("XDG_RUNTIME_DIR is unset")
		}
		n = filepath.Join(rt, n)
	}
	st, err := os.Lstat(n)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("%s is not a unix socket", n)
	}
	return n, nil
}

// DeviceArgs returns the bwrap arguments (and trailing --setenv pairs) for the
// domain's device grants. No grant, no arguments.
func DeviceArgs(m *Meta) (args, env []string) {
	if hasDevice(m, "gpu") {
		for _, n := range RenderNodes() {
			args = append(args, "--dev-bind", n, n)
		}
	}
	if hasDevice(m, "wayland") {
		if sock, err := WaylandSocket(); err == nil {
			args = append(args, "--bind", sock, DisplayPath)
			env = append(env, "--setenv", "WAYLAND_DISPLAY", DisplayPath)
		}
	}
	return
}

// DeviceWarnings says which grants composed to nothing, so a grant that does
// nothing is never silent.
func DeviceWarnings(m *Meta) []string {
	var w []string
	if hasDevice(m, "gpu") && len(RenderNodes()) == 0 {
		w = append(w, "device grant gpu: no /dev/dri/renderD* on this host, nothing bound")
	}
	if hasDevice(m, "gpu") {
		for _, n := range RenderNodes() {
			if st, err := os.Stat(n); err == nil && st.Mode().Perm()&0o006 != 0o006 {
				w = append(w, fmt.Sprintf("device grant gpu: %s is mode %04o; the domain runs as its own subordinate uid and can open it only if the node is world-accessible. A login-user ACL or the render group does not reach that uid, so the node is present but open() will fail with EACCES (decision 0014)", n, st.Mode().Perm()))
			}
		}
	}
	if hasDevice(m, "wayland") {
		if _, err := WaylandSocket(); err != nil {
			w = append(w, "device grant wayland: no compositor socket bound: "+err.Error())
		}
	}
	return w
}
