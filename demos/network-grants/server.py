"""A local test server for demos/network-grants/run.sh.

usage: server.py BIND PORT LABEL ACCEPTLOG [CERT KEY]

Answers every GET with "hello from LABEL". Appends one line to ACCEPTLOG for
every TCP connection it accepts, so the demo can prove that a refused request
never reached it (an empty log), not merely that the request failed. With CERT
and KEY it serves TLS.
"""
import http.server
import socketserver
import ssl
import sys

bind, port, label, log = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = ("hello from %s\n" % label).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def get_request(self):
        c, a = self.socket.accept()
        with open(log, "a") as f:
            f.write("%s:%s\n" % a)
        return c, a


srv = Server((bind, port), Handler)
if len(sys.argv) > 6:
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(sys.argv[5], sys.argv[6])
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
