"""
Guard tests: no benchmark harness may write to an instance that is not an eval instance.

Each harness is run as a real subprocess against a throwaway stub HTTP server that
reports a chosen ``env`` on /health and records every other request it receives.
A harness that passes these tests cannot have reached a prod-shaped target.

Run:  python -m pytest evaluation/locomo/tests/test_eval_guard.py -v
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
EVAL = REPO / "evaluation"
LOCOMO = EVAL / "locomo"
sys.path.insert(0, str(EVAL))
import eval_guard  # noqa: E402

PY_HARNESSES = {
    "ingest": [sys.executable, str(LOCOMO / "ingest.py"), "--sample"],
    "query": [sys.executable, str(LOCOMO / "query.py"), "--sample", "--out", os.devnull],
    "cleanup": [sys.executable, str(LOCOMO / "scripts" / "cleanup_locomo_cubes.py"), "--sample"],
}
SH_HARNESSES = {
    "locomo_run_sh": ["bash", str(LOCOMO / "run.sh")],
    "loadtest_run_sh": ["bash", str(REPO / "memdb-go" / "loadtest" / "run.sh"), "health"],
}


class StubMemDB:
    """Minimal memdb-go stand-in: /health reports ``env``; everything else is recorded."""

    def __init__(self, env: str | None):
        self.env = env
        self.writes: list[tuple[str, str, str | None]] = []  # (method, path, marker header)
        stub = self

        class H(BaseHTTPRequestHandler):
            def _reply(self, payload: dict) -> None:
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):  # noqa: N802
                if self.path == "/health":
                    data = {"service": "memdb-go", "status": "healthy"}
                    if stub.env is not None:
                        data["env"] = stub.env
                    return self._reply({"code": 200, "message": "ok", "data": data})
                stub.writes.append(("GET", self.path, self.headers.get(eval_guard.HARNESS_HEADER)))
                self._reply({"code": 200, "message": "ok", "data": []})

            def do_POST(self):  # noqa: N802
                n = int(self.headers.get("Content-Length") or 0)
                self.rfile.read(n)
                stub.writes.append(("POST", self.path, self.headers.get(eval_guard.HARNESS_HEADER)))
                self._reply({"code": 200, "message": "ok", "data": []})

            def log_message(self, *_a):
                pass

        self.server = HTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.server.server_port}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


def run(cmd: list[str], extra_args: list[str] | None = None, env_url: str | None = None):
    env = {k: v for k, v in os.environ.items() if k not in ("MEMDB_URL", "MEMOS_URL", "MEMDB_E2E_URL")}
    if env_url is not None:
        env["MEMDB_URL"] = env_url
    env["SERVICE_SECRET"] = "x"  # loadtest checks it after the guard; keep it out of the way
    return subprocess.run(
        cmd + (extra_args or []), env=env, capture_output=True, text=True, timeout=60, cwd=str(REPO)
    )


class TestNoDefaultTarget(unittest.TestCase):
    def test_python_harness_without_target_exits_nonzero(self):
        for name, cmd in PY_HARNESSES.items():
            with self.subTest(harness=name):
                r = run(cmd)
                self.assertEqual(r.returncode, eval_guard.EXIT_NO_TARGET, r.stderr)
                self.assertIn("no target", r.stderr)

    def test_shell_harness_without_target_exits_nonzero(self):
        for name, cmd in SH_HARNESSES.items():
            with self.subTest(harness=name):
                r = run(cmd)
                self.assertEqual(r.returncode, 2, r.stdout + r.stderr)

    def test_backfill_and_probe_without_container_exit_nonzero(self):
        for script in ("backfill_event_dates_regex.sh", "probe_empty_cube_search.sh"):
            with self.subTest(script=script):
                r = run(["bash", str(LOCOMO / "scripts" / script)])
                self.assertEqual(r.returncode, 2, r.stdout + r.stderr)


class TestRefusesNonEvalInstance(unittest.TestCase):
    def _assert_refused(self, stub: StubMemDB, cmd: list[str], via_env: bool, code: int = 3):
        try:
            if via_env:
                r = run(cmd, env_url=stub.url)
            else:
                r = run(cmd, ["--memdb-url", stub.url])
            self.assertEqual(r.returncode, code, r.stdout + r.stderr)
            self.assertEqual(stub.writes, [], "harness wrote to a non-eval instance")
        finally:
            stub.close()

    def test_ingest_refuses_prod_instance(self):
        self._assert_refused(StubMemDB("prod"), PY_HARNESSES["ingest"], via_env=False)

    def test_query_refuses_prod_instance(self):
        self._assert_refused(StubMemDB("prod"), PY_HARNESSES["query"], via_env=False)

    def test_cleanup_refuses_prod_instance(self):
        self._assert_refused(StubMemDB("prod"), PY_HARNESSES["cleanup"], via_env=False)

    def test_locomo_run_sh_refuses_prod_instance(self):
        self._assert_refused(StubMemDB("prod"), SH_HARNESSES["locomo_run_sh"], via_env=True)

    def test_loadtest_refuses_prod_instance(self):
        self._assert_refused(StubMemDB("prod"), SH_HARNESSES["loadtest_run_sh"], via_env=True)

    def test_instance_without_env_field_is_refused(self):
        # A build that predates the guard reports no env; it must be treated as not-eval.
        self._assert_refused(StubMemDB(None), PY_HARNESSES["ingest"], via_env=False)

    def test_unreachable_instance_is_refused(self):
        stub = StubMemDB("eval")
        url = stub.url
        stub.close()  # nothing listens any more
        r = run(PY_HARNESSES["ingest"], ["--memdb-url", url])
        self.assertEqual(r.returncode, eval_guard.EXIT_NOT_EVAL, r.stderr)


class TestAcceptsEvalInstance(unittest.TestCase):
    def setUp(self):
        self.stub = StubMemDB("eval")
        self.addCleanup(self.stub.close)

    def test_guard_returns_url_for_eval_instance(self):
        self.assertEqual(eval_guard.guard(self.stub.url + "/"), self.stub.url + "/")

    def test_cleanup_reaches_eval_instance_with_marker(self):
        r = run(PY_HARNESSES["cleanup"], ["--memdb-url", self.stub.url])
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.stub.writes, "guard blocked a legitimate eval target")
        self.assertTrue(all(m == "1" for _, _, m in self.stub.writes), self.stub.writes)

    def test_shell_guard_accepts_eval_instance(self):
        r = subprocess.run(
            ["bash", "-c", f'source "{EVAL}/eval_guard.sh"; eval_guard_target "{self.stub.url}"'],
            capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_every_harness_header_builder_carries_marker(self):
        sys.path.insert(0, str(LOCOMO))
        import ingest
        import query

        for mod in (ingest, query):
            self.assertEqual(mod.build_headers().get(eval_guard.HARNESS_HEADER), "1", mod.__name__)


if __name__ == "__main__":
    unittest.main()
