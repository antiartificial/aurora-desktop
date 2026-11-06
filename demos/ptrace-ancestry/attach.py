#!/usr/bin/env python3
"""PTRACE_ATTACH to a pid, detach immediately, report which happened.
Attaching proves an attacker could inject code; it changes nothing."""
import ctypes, ctypes.util, errno, os, sys

libc = ctypes.CDLL(ctypes.util.find_library("c"), use_errno=True)
PTRACE_ATTACH, PTRACE_DETACH = 16, 17
pid = int(sys.argv[1])
ctypes.set_errno(0)
if libc.ptrace(PTRACE_ATTACH, pid, None, None) == 0:
    os.waitpid(pid, 0)
    libc.ptrace(PTRACE_DETACH, pid, None, None)
    print("ATTACHED")
else:
    e = ctypes.get_errno()
    print(f"DENIED ({errno.errorcode.get(e, e)})")
