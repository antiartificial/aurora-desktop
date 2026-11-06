#!/usr/bin/python3
"""Send commands to the compositor's --test-input socket and print each reply.
usage: python3 -I ti.py SOCKET CMD...      e.g.  ti.py $RUN/ti "key h" "down shift" "key l" "up shift"
Exits non-zero if any reply does not start with `ok` (a refused key must fail the run, not pass silently)."""
import socket, sys, time
s = socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sys.argv[1]); f = s.makefile('rw'); bad = 0
for cmd in sys.argv[2:]:
    f.write(cmd + '\n'); f.flush(); r = f.readline().strip(); print(f'{cmd} -> {r}')
    bad += not r.startswith('ok'); time.sleep(0.15)
sys.exit(1 if bad else 0)
