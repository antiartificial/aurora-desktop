#!/usr/bin/env python3
"""A stand-in for controllerd's drop socket, for the drag-and-drop demo.

The real controller (`controllerd --drop-socket`) answers a drop by mounting the dropped files
into the target's domain and minting a powerbox-class ticket per path (decision 0020, step 3).
Nothing in this repo mounts into a RUNNING domain yet, so this answers the same wire format
(`controller/testdata/drop-wire.json`) with the paths a mount would produce, and writes every
request it was asked to a log the demo asserts on.

It grants read-only, for exactly the paths asked about, and nothing else. It is a demo prop: it
never touches the filesystem.

  controller-standin.py SOCKET REQUESTLOG
"""

import json
import os
import socket
import sys

sock, reqlog = sys.argv[1], sys.argv[2]
if os.path.exists(sock):
    os.unlink(sock)
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(sock)
s.listen(8)
print(f"standin controller listening on {sock}", flush=True)

while True:
    c, _ = s.accept()
    with c:
        line = c.makefile("r").readline()
        if not line:
            continue
        with open(reqlog, "a") as f:
            f.write(line if line.endswith("\n") else line + "\n")
        try:
            req = json.loads(line)
        except ValueError:
            c.sendall(b'{"v":1,"ok":false,"code":"bad_request","error":"not json"}\n')
            continue
        grants = [
            {
                "id": str(i + 1),
                "host": p,
                "guest": f"/run/aurora/drops/{i + 1}/{os.path.basename(p)}",
                "mode": "ro",
                "dir": False,
                "source": "powerbox",
                "ticket": "demo",
            }
            for i, p in enumerate(req.get("paths", []))
        ]
        reply = {"v": 1, "ok": True, "app": "org.aurora.demo", "domain": req.get("domain", ""), "grants": grants}
        c.sendall((json.dumps(reply) + "\n").encode())
