#!/usr/bin/python3
"""Send commands to the compositor's --test-input socket; print each reply. Exits non-zero if any reply is not `ok`.
usage: python3 -I ti.py SOCKET [--pause SECONDS] CMD...    e.g.  ti.py $RUN/ti "pointer 40 60" press "sleep 0.3" release
`sleep S` is local to this script (a held button, a drag that needs time between moves). Commands are the ones in
compositor/src/trusted/testinput.rs: key/down/up NAME, pointer X Y, click, rclick, press, release, rpress, rrelease, move N X Y."""
import socket, sys, time
args = sys.argv[2:]; pause = 0.12
if args[:1] == ['--pause']: pause = float(args[1]); args = args[2:]
s = socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sys.argv[1]); f = s.makefile('rw'); bad = 0
for cmd in args:
    if cmd.startswith('sleep '): time.sleep(float(cmd.split()[1])); continue
    f.write(cmd + '\n'); f.flush(); r = f.readline().strip(); print(f'{cmd} -> {r}')
    bad += not r.startswith('ok'); time.sleep(pause)
sys.exit(1 if bad else 0)
