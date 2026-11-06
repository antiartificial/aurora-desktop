# Same-uid attacker toolkit for bus/demo-identity.sh (run as the invoking user).
#   atk.py setuid  UID                       try to become UID with plain setuid(2) from the host
#   atk.py forge   SOCK CLAIM INNER [CG]     (run inside `unshare --user` mapped over the subuid range)
#                                            take euid INNER (= the domain's subuid), keep fsuid 0, connect to SOCK
#   atk.py setns   SOCK CLAIM CG TGT [INNER] join CG, setns into TGT's namespaces (and, with INNER, take that euid), connect to SOCK as seen from inside
import ctypes, fcntl, os, socket, sys, time

def talk(path, claim):
    s = socket.socket(socket.AF_UNIX)
    s.connect(path)
    s.settimeout(0.5)
    s.sendall(b'{"kind":"event","topic":"demo.spoof","from":"' + claim.encode() + b'"}\n')
    try:
        print("daemon replied: " + s.recv(4096).decode().strip())
    except Exception as e:
        print("daemon replied nothing:", e)
    print("  (my uid as the kernel sees it: euid=%d in my namespace)" % os.geteuid())

def becomes(inner):
    os.seteuid(inner)                      # SO_PEERCRED reads the effective uid
    libc = ctypes.CDLL(None, use_errno=True)
    libc.setfsuid(0)                        # file access stays as namespace root (= the real user)
    libc.prctl(4, 1, 0, 0, 0)               # PR_SET_DUMPABLE: changing euid cleared it, and the daemon could then not read /proc/<pid>/ns

m = sys.argv[1]
if m == "setuid":
    try:
        os.setuid(int(sys.argv[2])); print("became uid", sys.argv[2])
    except OSError as e:
        print("setuid(%s): %s" % (sys.argv[2], e))
elif m == "forge":
    sock, claim, inner = sys.argv[2], sys.argv[3], int(sys.argv[4])
    if len(sys.argv) > 5:
        open(sys.argv[5] + "/cgroup.procs", "w").write("0")   # join the cgroup first, as namespace root (the real user)
    becomes(inner)
    talk(sock, claim)
elif m == "setns":
    sock, claim, cg, tgt = sys.argv[2:6]
    inner = int(sys.argv[6]) if len(sys.argv) > 6 else None
    try:
        o = lambda t: os.open("/proc/%s/ns/%s" % (tgt, t), os.O_RDONLY)
        ipc, uts, mnt, pid = o("ipc"), o("uts"), o("mnt"), o("pid")
        owner = fcntl.ioctl(ipc, 0xb701)            # NS_GET_USERNS: the user namespace owning the sandbox
        open(cg + "/cgroup.procs", "w").write("0")
        os.setns(owner, os.CLONE_NEWUSER)
        for fd, f in ((ipc, os.CLONE_NEWIPC), (uts, os.CLONE_NEWUTS), (mnt, os.CLONE_NEWNS), (pid, os.CLONE_NEWPID)):
            os.setns(fd, f)
    except (OSError, AttributeError) as e:
        print("attacker could not enter the namespaces:", e); sys.exit(0)
    if os.fork() == 0:
        try:
            if inner is not None:
                becomes(inner)
            talk(sock, claim)
        except OSError as e:
            print("attacker failed:", e)
        os._exit(0)
    os.wait()
