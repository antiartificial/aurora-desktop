package invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/prov"
	"github.com/antiartificial/aurora/controller/ticket"
)

// A file dropped on a window is a grant (decision 0020, "The grant"). The compositor saw the drop, read
// the uri-list once, resolved it, and tells us which domain the kernel put the target client in and
// which paths the user dropped. What it cannot do is mount anything: grants are the controller's. This
// is the verb that mounts into a RUNNING domain, which until now did not exist (grants were decided at
// invoke and composed into the next run).
//
// What decides, in order:
//  1. The domain must be a window THIS controller launched and that is still running. The app is read
//     from our own launch table, never from the request: the compositor derives the domain from the
//     client's cgroup, and the domain name is what we chose at launch.
//  2. Every path is re-checked here. The compositor is not trusted to have validated anything:
//     absolute, already canonical, a regular file or directory, readable by the user, not a key we hold.
//  3. Read-only of a regular file needs no approval beyond the drop (0020 step 6). A directory, or
//     read-write, goes through the same approval prompt a destructive verb does, because one drop
//     would otherwise grant a tree or the right to change a file.
//  4. A powerbox-class ticket is minted per path (app, verb "drop", mode, exact path, single use) and
//     redeemed on the spot. That is bookkeeping, not a second check: the controller holds the key, so
//     a ticket it mints for itself proves nothing a caller could not forge. It is the same record a
//     picker's ticket leaves (source "powerbox") and the ledger makes one authorisation one mount.
//  5. The adapter mounts it (domain drop), per drop: each drop is its own mount under its own number,
//     revocable on its own, and all of them are unmounted when the window ends.

// DropVerb is the verb name a drop is recorded and ticketed under.
const DropVerb = "drop"

// MaxDropPaths bounds one drop. A drag of a whole folder's worth of files is a different feature.
const MaxDropPaths = 16

// DropRequest is what the compositor sends. App is optional and, when set, must agree with our launch table.
type DropRequest struct {
	Op     string   `json:"op"` // "drop" (default) or "release"
	Domain string   `json:"domain"`
	App    string   `json:"app,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	Mode   string   `json:"mode,omitempty"` // "ro" (default) or "rw"
	// ID is the drop to release (op "release").
	ID string `json:"id,omitempty"`
}

// DropGrant is one path now mounted in the domain.
type DropGrant struct {
	ID     string `json:"id"`
	Host   string `json:"host"`
	Guest  string `json:"guest"`
	Mode   string `json:"mode"`
	Dir    bool   `json:"dir"`
	Source string `json:"source"`
	Ticket string `json:"ticket"` // the ticket's nonce, an id and not a credential
	Cap    string `json:"cap,omitempty"`
}

// DropRefusal says why one path was not mounted.
type DropRefusal struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// DropResult is the success reply. A drop of several paths can succeed for some: Refused names the rest.
type DropResult struct {
	OK       bool           `json:"ok"`
	App      string         `json:"app"`
	Domain   string         `json:"domain"`
	Grants   []DropGrant    `json:"grants"`
	Refused  []DropRefusal  `json:"refused,omitempty"`
	Approval *prov.Approval `json:"approval,omitempty"`
}

type dropRec struct {
	DropGrant
	caps *capSet
}

type dropState struct {
	mu    sync.Mutex
	drops map[string][]*dropRec // domain name -> live drops
}

func (iv *Invoker) dropState() *dropState {
	iv.dropOnce.Do(func() { iv.dstate = &dropState{drops: map[string][]*dropRec{}} })
	return iv.dstate
}

// windowApp is the app of a running window, read from the launch table.
func (iv *Invoker) windowApp(domainName string) (string, bool) {
	s := iv.launches()
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.running[domainName]
	if !ok {
		return "", false
	}
	return l.app, true
}

// checkDropPath says whether one path may be mounted and what it is. It opens nothing that can block.
func (iv *Invoker) checkDropPath(p string) (dir bool, err error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return false, fmt.Errorf("not an absolute clean path")
	}
	if r, err := filepath.EvalSymlinks(p); err != nil {
		return false, fmt.Errorf("does not exist or cannot be resolved")
	} else if r != p {
		return false, fmt.Errorf("is not canonical (resolves to %s); the drop must name the resolved file", r)
	}
	for _, d := range append([]string{"/proc", "/sys", "/dev"}, iv.DropDeny...) {
		if within(d, p) {
			return false, fmt.Errorf("is not something a drop may give away (%s)", d)
		}
	}
	st, err := os.Lstat(p)
	if err != nil {
		return false, err
	}
	switch {
	case st.IsDir():
		dir = true
	case st.Mode().IsRegular():
	default:
		return false, fmt.Errorf("is not a regular file or directory (%s)", st.Mode().Type())
	}
	// Readable by this user, as the kernel says it. O_NONBLOCK so nothing the type check missed can hang us.
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("cannot be read by the user (%v)", err)
	}
	syscall.Close(fd)
	return dir, nil
}

// spendTicket mints the powerbox-class ticket for one path and redeems it. See the comment at the top of this file.
func (iv *Invoker) spendTicket(app, path, mode string) (string, *Error) {
	if iv.Tickets == nil {
		return "", fail("ticket_unavailable", "this controller has no powerbox key, so it cannot record a drop as a grant", "")
	}
	now := iv.Tickets.now()
	tok, err := ticket.Mint(iv.Tickets.Key, ticket.Claims{Path: path, Mode: mode, App: app, Verb: DropVerb}, now, ticket.DefaultTTL)
	if err != nil {
		return "", fail("ticket_unavailable", "cannot mint a ticket", err.Error())
	}
	cl, err := ticket.Verify(iv.Tickets.Key, tok, now)
	if err == nil {
		err = cl.Check(ticket.Want{Path: path, Mode: mode, App: app, Verb: DropVerb})
	}
	if err == nil {
		err = iv.Tickets.ledger.Redeem(now, cl)
	}
	if err != nil {
		return "", fail("ticket_refused", "the drop's ticket was refused", err.Error())
	}
	return cl.Nonce, nil
}

// Drop mounts the dropped paths into a running window's domain. See the comment at the top of this file.
func (iv *Invoker) Drop(ctx context.Context, req DropRequest) (*DropResult, *Error) {
	if req.Mode == "" {
		req.Mode = "ro"
	}
	if req.Mode != "ro" && req.Mode != "rw" {
		return nil, fail("bad_request", "mode must be ro or rw, got %q", "", req.Mode)
	}
	if len(req.Paths) == 0 || len(req.Paths) > MaxDropPaths {
		return nil, fail("bad_request", "a drop names 1 to %d paths, got %d", "", MaxDropPaths, len(req.Paths))
	}
	appID, ok := iv.windowApp(req.Domain)
	if !ok {
		return nil, fail("no_such_window", "domain %q is not a window this controller launched and is still running", "a drop is a grant to a running window's domain; there is nothing to mount into", req.Domain)
	}
	if req.App != "" && req.App != appID {
		return nil, fail("app_mismatch", "domain %s runs %s, not %s", "", req.Domain, appID, req.App)
	}
	app, ok := iv.Reg.App(appID)
	if !ok {
		return nil, fail("no_such_app", "no such app: %q", "", appID)
	}
	if err := iv.Runner.Available(); err != nil {
		return nil, fail("isolation_unavailable", "domain tooling unavailable", err.Error())
	}

	res := &DropResult{App: appID, Domain: req.Domain}
	type item struct {
		path string
		dir  bool
	}
	var items []item
	seen := map[string]bool{}
	for _, p := range req.Paths {
		dir, err := iv.checkDropPath(p)
		if err != nil {
			res.Refused = append(res.Refused, DropRefusal{p, err.Error()})
			continue
		}
		if !seen[p] {
			seen[p] = true
			items = append(items, item{p, dir})
		}
	}
	if len(items) == 0 {
		return nil, fail("drop_refused", "nothing in the drop can be granted", refusals(res.Refused))
	}

	// Approval (0020 step 6): a directory or read-write only. The grants shown are exactly what will be mounted.
	needs, effect := false, manifest.EffectExternal
	var grants []prov.Grant
	for _, it := range items {
		grants = append(grants, prov.Grant{Path: it.path, Mode: req.Mode, Source: prov.SourcePowerbox})
		if it.dir || req.Mode == "rw" {
			needs = true
		}
	}
	if req.Mode == "rw" {
		effect = manifest.EffectDestructive
	}
	if needs {
		ap, e := iv.approveIn(ctx, app, req.Domain, manifest.Verb{Name: DropVerb, Effect: effect}, DropVerb, mustJSON(map[string]any{"drop": req.Paths}), grants)
		if e != nil {
			return nil, e
		}
		res.Approval = ap
	}

	st := iv.dropState()
	for _, it := range items {
		// The window may have ended while a prompt was open.
		if _, still := iv.windowApp(req.Domain); !still {
			return nil, fail("no_such_window", "domain %q ended before the drop could be mounted", "", req.Domain)
		}
		nonce, e := iv.spendTicket(appID, it.path, req.Mode)
		if e != nil {
			res.Refused = append(res.Refused, DropRefusal{it.path, e.Msg})
			continue
		}
		m, err := iv.Runner.MountDrop(req.Domain, it.path, req.Mode)
		if err != nil {
			iv.logf("drop: mounting %s into %s failed: %v", it.path, req.Domain, err)
			res.Refused = append(res.Refused, DropRefusal{it.path, "could not be mounted into the domain: " + err.Error()})
			continue
		}
		g := prov.Grant{Path: it.path, Mode: req.Mode, Source: prov.SourcePowerbox}
		gs := []prov.Grant{g}
		cs := iv.mintCapsTTL(gs, 0)
		ident, _ := iv.domainIdentity(req.Domain)
		iv.announceHeld(gs[0], ident, appID)
		rec := &dropRec{DropGrant: DropGrant{ID: m.ID, Host: it.path, Guest: m.Guest, Mode: req.Mode, Dir: m.Dir, Source: prov.SourcePowerbox, Ticket: nonce, Cap: gs[0].Cap}, caps: cs}
		st.mu.Lock()
		st.drops[req.Domain] = append(st.drops[req.Domain], rec)
		st.mu.Unlock()
		res.Grants = append(res.Grants, rec.DropGrant)
		iv.logf("drop: %s (%s) mounted into %s of %s at %s, ticket %s, approval %s", it.path, req.Mode, req.Domain, appID, m.Guest, nonce, approvalNote(res.Approval))
	}
	if len(res.Grants) == 0 {
		return nil, fail("drop_refused", "nothing in the drop could be granted", refusals(res.Refused))
	}
	res.OK = true
	return res, nil
}

func approvalNote(a *prov.Approval) string {
	if a == nil {
		return "not needed (read-only regular file)"
	}
	return a.Route
}

func refusals(r []DropRefusal) string {
	var s []string
	for _, x := range r {
		s = append(s, x.Path+": "+x.Reason)
	}
	return strings.Join(s, "; ")
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ReleaseDrop unmounts one drop of a window and revokes its handle: the revocation of a drop is unmounting.
func (iv *Invoker) ReleaseDrop(domainName, id string) *Error {
	st := iv.dropState()
	st.mu.Lock()
	var rec *dropRec
	list := st.drops[domainName]
	for i, r := range list {
		if r.ID == id {
			rec = r
			st.drops[domainName] = append(append([]*dropRec(nil), list[:i]...), list[i+1:]...)
			break
		}
	}
	st.mu.Unlock()
	if rec == nil {
		return fail("no_such_drop", "domain %s has no drop %q", "", domainName, id)
	}
	err := iv.Runner.UnmountDrop(domainName, id)
	rec.caps.release(iv.logf)
	if err != nil {
		return fail("domain_error", "unmounting drop %s of %s failed", err.Error(), id, domainName)
	}
	iv.logf("drop: %s of %s (%s) released", id, domainName, rec.Host)
	return nil
}

// releaseDrops unmounts everything dropped on a window. Called as the window's domain is torn down.
func (iv *Invoker) releaseDrops(domainName string) {
	st := iv.dropState()
	st.mu.Lock()
	list := st.drops[domainName]
	delete(st.drops, domainName)
	st.mu.Unlock()
	for _, r := range list {
		if err := iv.Runner.UnmountDrop(domainName, r.ID); err != nil {
			iv.logf("WARNING: drop %s of %s was not unmounted cleanly: %v", r.ID, domainName, err)
		}
		r.caps.release(iv.logf)
	}
}

// Drops lists a window's live drops (for tests and for status).
func (iv *Invoker) Drops(domainName string) []DropGrant {
	st := iv.dropState()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []DropGrant
	for _, r := range st.drops[domainName] {
		out = append(out, r.DropGrant)
	}
	return out
}
