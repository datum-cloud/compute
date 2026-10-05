"""Minimal stdlib HTTP service for the Datum compute Python runtime proof.

No third-party dependencies -- only the CPython standard library
(http.server) so the rootfs needs nothing but the interpreter itself.
Serves on $PORT (default 8080):
  /healthz -> "ok"
  anything else -> "Hello from Datum (Python)"
Prints "listening on :<port>" on start as a boot marker on the console.
"""

import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def _respond(self, body):
        payload = body.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == "/healthz":
            self._respond("ok\n")
        else:
            self._respond("Hello from Datum (Python)\n")

    # Quiet the default request logging noise on the console.
    def log_message(self, format, *args):
        pass


def main():
    port = int(os.environ.get("PORT", "8080"))
    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    print("listening on :%d" % port, flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
