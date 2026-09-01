#!/usr/bin/env python3
"""Mock Hermes Runs API for hermes-hands tests. No network, no model.

MODE:
  plain      -> every run answers with {"calls":[],"final":"..."}
  delegate   -> run 1 asks read_file+run; run 2 (seeing results) answers final
  badjson    -> run 1 emits broken JSON with a "calls" hint; run 2 emits valid
  prose      -> run 1 answers in plain prose (no envelope) -> loop accepts it
"""
import json, os, re, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ["PORT"])
MODE = os.environ.get("MODE", "plain")
RUNS = {}
_n = {"i": 0}


class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def _s(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.endswith("/v1/capabilities"):
            return self._s(200, {"model": "hermes-agent", "runtime": {"mode": "server_agent"}})
        m = re.match(r"^/(?:p/[^/]+/)?v1/runs/([^/]+)$", self.path)
        if m:
            r = RUNS.get(m.group(1))
            if not r:
                return self._s(404, {"error": "unknown run"})
            return self._s(200, {"run_id": m.group(1), "status": "completed",
                                 "session_id": "sess-1", "output": r["output"]})
        self._s(404, {"error": "nf"})

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(n) or b"{}")
        if not re.match(r"^/(?:p/[^/]+/)?v1/runs$", self.path):
            return self._s(404, {"error": "nf"})
        inp0 = body.get("input") or ""
        seen_results = '"results"' in inp0
        got_fixup = '"error"' in inp0 and '"results"' not in inp0
        _n["i"] += 1
        rid = f"run_{_n['i']}"

        if MODE == "plain":
            out = json.dumps({"calls": [], "final": "plain answer from the mock brain."})
        elif MODE == "prose":
            out = "Just a plain-prose answer, no envelope at all."
        elif MODE == "badjson":
            if not seen_results and not got_fixup:
                out = "```json\n{\"calls\": [ {\"tool\": \"read_file\", }  ]  // oops\n```"
            else:
                out = json.dumps({"calls": [], "final": "recovered and answered."})
        elif MODE == "delegate":
            if not seen_results:
                out = json.dumps({"calls": [
                    {"tool": "read_file", "args": {"path": "README.md"}},
                    {"tool": "shell", "args": {"cmd": "git rev-parse --abbrev-ref HEAD"}},
                ], "final": None})
            else:
                inp = body.get("input", "")
                ok = ('"exit_code": 0' in inp or '"exit_code":0' in inp)
                out = json.dumps({"calls": [], "final": f"done. saw_results={ok}."})

        elif MODE == "shellstate":
            if not seen_results:
                out = json.dumps({"calls": [
                    {"tool": "shell", "args": {"cmd": "mkdir -p sub && cd sub"}},
                    {"tool": "shell", "args": {"cmd": "pwd"}},
                ], "final": None})
            else:
                inp = body.get("input", "")
                out = json.dumps({"calls": [], "final": f"cwd_persisted={'/sub' in inp}"})
        else:
            out = json.dumps({"calls": [], "final": "?"})

        RUNS[rid] = {"output": out}
        self._s(200, {"run_id": rid, "status": "started"})


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), H).serve_forever()
