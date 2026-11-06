// Command domain is the Linux backend CLI for the kernel adapter (contract 3).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/antiartificial/aurora/adapter/linux/internal/dom"
)

const usage = `usage: domain <command> [args]

  create <name>                         create a domain (btrfs subvolumes if available)
  run <name> -- <cmd...>                run a command inside the domain
  grant <name> <path> [--ro|--rw] [--exact]
                                        grant one file or directory (default --ro); --exact binds
                                        the path as given and refuses it unless already canonical
  grant <name> --net <host>             grant network (coarse: shares the host network)
  revoke <name> <path>                  remove a path grant
  revoke <name> --net <host>            remove a net grant
  list                                  list domains
  show <name>                           grants, storage paths, limits, snapshots
  snapshot <name> [--tag t]             btrfs snapshot of data/config/cache
  rollback <name> <snapshot>            restore a snapshot
  limit <name> [--memory 512M] [--pids N] [--cpu PCT]   cgroup2 limits
  destroy <name> [--keep-data]          remove the domain
  exec-id <name>                        print the attested domain identity
  cgroup <name>                         print the domain's cgroup (as /proc/<pid>/cgroup shows it); the bus attests membership
  nsid <name>                           print the namespaces (type:inode) of the domain's live run; the bus attests them too
  sockpath <name>                       print the host path of the domain's bus socket

data dir: $AURORA_DOMAINS_DIR or ${XDG_DATA_HOME:-~/.local/share}/aurora/domains
`

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "domain: "+format+"\n", a...)
	os.Exit(1)
}

// parse lets flags appear before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) []string {
	fs.SetOutput(os.Stderr)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func need(pos []string, n int, what string) {
	if len(pos) != n {
		die("expected %s", what)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	st := dom.Store{Root: dom.DefaultRoot()}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
	case "create":
		pos := parse(fs, args)
		need(pos, 1, "create <name>")
		m, note, err := st.Create(pos[0])
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("created domain %s (id %s)\n  path: %s\n  %s\n", m.Name, m.ID, st.Dir(m.Name), note)
	case "run":
		if len(args) < 3 || args[1] != "--" {
			die("usage: domain run <name> -- <command...>")
		}
		code, err := st.Run(args[0], args[2:], os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			die("%v", err)
		}
		os.Exit(code)
	case "grant":
		ro, rw := fs.Bool("ro", false, "read-only (default)"), fs.Bool("rw", false, "read-write")
		net := fs.String("net", "", "network host to grant")
		exact := fs.Bool("exact", false, "path is already canonical: bind it as given, refuse if it is not")
		pos := parse(fs, args)
		if *net != "" {
			need(pos, 1, "grant <name> --net <host>")
			if err := st.GrantNet(pos[0], *net); err != nil {
				die("%v", err)
			}
			fmt.Printf("granted net %s to %s\n  NOTE: enforcement is coarse. Any net grant gives the domain the host network namespace;\n  the host name is recorded but NOT filtered.\n", *net, pos[0])
			return
		}
		need(pos, 2, "grant <name> <path>")
		if *ro && *rw {
			die("--ro and --rw are exclusive")
		}
		mode := "ro"
		if *rw {
			mode = "rw"
		}
		grant := st.GrantPath
		if *exact {
			grant = st.GrantPathExact
		}
		p, err := grant(pos[0], pos[1], mode)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("granted %s (%s) to %s\n", p, mode, pos[0])
	case "revoke":
		net := fs.Bool("net", false, "target is a net host")
		pos := parse(fs, args)
		need(pos, 2, "revoke <name> <path>  |  revoke <name> --net <host>")
		if err := st.Revoke(pos[0], pos[1], *net); err != nil {
			die("%v", err)
		}
		fmt.Printf("revoked %s from %s\n", pos[1], pos[0])
	case "list":
		ms, err := st.List()
		if err != nil {
			die("%v", err)
		}
		for _, m := range ms {
			fmt.Printf("%-20s %s  grants=%d  storage=%s\n", m.Name, m.ID, len(m.Grants), storage(m))
		}
		if len(ms) == 0 {
			fmt.Println("(no domains)")
		}
	case "show":
		pos := parse(fs, args)
		need(pos, 1, "show <name>")
		show(st, pos[0])
	case "snapshot":
		tag := fs.String("tag", "", "snapshot tag")
		pos := parse(fs, args)
		need(pos, 1, "snapshot <name> [--tag t]")
		t, err := st.Snapshot(pos[0], *tag)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("snapshot %s of %s created\n", t, pos[0])
	case "rollback":
		pos := parse(fs, args)
		need(pos, 2, "rollback <name> <snapshot>")
		if err := st.Rollback(pos[0], pos[1]); err != nil {
			die("%v", err)
		}
		fmt.Printf("rolled %s back to snapshot %s\n", pos[0], pos[1])
	case "limit":
		mem := fs.String("memory", "", "memory limit, e.g. 512M")
		pids := fs.Int("pids", -1, "max processes")
		cpu := fs.Int("cpu", -1, "CPU percent of one core")
		pos := parse(fs, args)
		need(pos, 1, "limit <name> [--memory 512M] [--pids N] [--cpu PCT]")
		limit(st, pos[0], *mem, *pids, *cpu)
	case "destroy":
		keep := fs.Bool("keep-data", false, "keep the data (renamed) instead of deleting")
		pos := parse(fs, args)
		need(pos, 1, "destroy <name> [--keep-data]")
		kept, err := st.Destroy(pos[0], *keep)
		if err != nil {
			die("%v", err)
		}
		if kept != "" {
			fmt.Printf("destroyed %s; data kept at %s\n", pos[0], kept)
		} else {
			fmt.Printf("destroyed %s\n", pos[0])
		}
	case "exec-id":
		pos := parse(fs, args)
		need(pos, 1, "exec-id <name>")
		m, err := st.Load(pos[0])
		if err != nil {
			die("%v", err)
		}
		fmt.Println(dom.ExecID(m))
	case "cgroup":
		pos := parse(fs, args)
		need(pos, 1, "cgroup <name>")
		if _, err := st.Load(pos[0]); err != nil {
			die("%v", err)
		}
		p, err := dom.CgroupPath(pos[0])
		if err != nil {
			die("no attestable cgroup on this host: %v", err)
		}
		fmt.Println(p)
	case "nsid":
		pos := parse(fs, args)
		need(pos, 1, "nsid <name>")
		ids, err := st.NSID(pos[0])
		if err != nil {
			die("%v", err)
		}
		fmt.Println(strings.Join(ids, "\n"))
	case "sockpath":
		pos := parse(fs, args)
		need(pos, 1, "sockpath <name>")
		if _, err := st.Load(pos[0]); err != nil {
			die("%v", err)
		}
		fmt.Println(dom.SockDir(pos[0]) + "/bus.sock")
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func storage(m *dom.Meta) string {
	if m.Subvolumes {
		return "btrfs"
	}
	return "plain"
}

func show(st dom.Store, name string) {
	m, err := st.Load(name)
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("name:      %s\nid:        %s\nexec-id:   %s\ncreated:   %s\nstorage:   %s\n", m.Name, m.ID, dom.ExecID(m), m.Created.Format("2006-01-02 15:04:05Z"), storage(m))
	for _, sd := range dom.Subdirs {
		fmt.Printf("  %-7s  %s\n", sd, st.Dir(name)+"/"+sd)
	}
	fmt.Printf("bus socket: %s/bus.sock\ngrants:\n", dom.SockDir(name))
	for _, g := range m.Grants {
		if g.Kind == "net" {
			fmt.Printf("  net   %s (coarse: host netns)\n", g.Host)
		} else {
			fmt.Printf("  path  %s (%s)\n", g.Path, g.Mode)
		}
	}
	if len(m.Grants) == 0 {
		fmt.Println("  (none)")
	}
	l := m.Limits
	fmt.Printf("limits:    memory=%s pids=%s cpu=%s\n", orMax(l.MemoryBytes > 0, fmt.Sprintf("%d bytes", l.MemoryBytes)),
		orMax(l.Pids > 0, fmt.Sprint(l.Pids)), orMax(l.CPUPercent > 0, fmt.Sprintf("%d%%", l.CPUPercent)))
	fmt.Printf("snapshots: %s\n", strings.Join(append(st.Snapshots(name), ""), " "))
}

func orMax(set bool, s string) string {
	if set {
		return s
	}
	return "unlimited"
}

func limit(st dom.Store, name, mem string, pids, cpu int) {
	m, err := st.Load(name)
	if err != nil {
		die("%v", err)
	}
	l := m.Limits
	if mem != "" {
		if l.MemoryBytes, err = dom.ParseSize(mem); err != nil {
			die("bad --memory %q", mem)
		}
	}
	if pids >= 0 {
		l.Pids = pids
	}
	if cpu >= 0 {
		l.CPUPercent = cpu
	}
	if err := st.SetLimits(name, l); err != nil {
		die("%v", err)
	}
	fmt.Printf("recorded limits for %s: memory=%d bytes pids=%d cpu=%d%% (0 = unlimited)\n", name, l.MemoryBytes, l.Pids, l.CPUPercent)
	bad := 0
	for _, r := range dom.ApplyLimits(name, l) {
		if r.Applied {
			fmt.Printf("  ENFORCED   %-6s %s\n", r.Controller, r.Detail)
		} else {
			bad++
			fmt.Printf("  NOT ENFORCED %-6s %s\n", r.Controller, r.Detail)
		}
	}
	if bad > 0 {
		fmt.Println("some limits are recorded but this host does not let an unprivileged user enforce them")
		os.Exit(3)
	}
}
