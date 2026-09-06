from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import sys
import threading
import time
import unittest


SUPPORT = Path(__file__).resolve().parents[2] / "support"
sys.path.insert(0, str(SUPPORT))

from ownward_mcp import MCPError, StreamableHTTPClient, product_instructions  # noqa: E402


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    client_ports: list[int] = []
    requests: list[dict] = []
    instructions: str | None = None
    rules: object = None
    delays: dict[str, float] = {}

    def handle(self) -> None:
        try:
            super().handle()
        except (ConnectionResetError, ConnectionAbortedError):
            pass  # The timeout test deliberately closes an in-flight request.

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        payload = json.loads(self.rfile.read(length))
        type(self).client_ports.append(self.client_address[1])
        type(self).requests.append(payload)
        time.sleep(type(self).delays.get(payload.get("params", {}).get("name", ""), 0))
        if payload.get("method") == "notifications/initialized":
            self._write(202, b"")
            return
        if payload.get("method") == "initialize":
            result = {"protocolVersion": "2025-06-18", "capabilities": {}, "serverInfo": {"name": "test", "version": "1"}}
            if type(self).instructions is not None:
                result["instructions"] = type(self).instructions
        elif payload.get("params", {}).get("name") == "ownward_rules":
            result = {"structuredContent": {"rules": type(self).rules}}
        else:
            result = {"structuredContent": {"ok": True}}
        self._write(200, json.dumps({"jsonrpc": "2.0", "id": payload["id"], "result": result}).encode())

    def do_DELETE(self) -> None:  # noqa: N802
        self._write(200, b"{}")

    def _write(self, status: int, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Mcp-Session-Id", "stable-session")
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            pass

    def log_message(self, format: str, *args: object) -> None:
        return


class StreamableHTTPClientTests(unittest.TestCase):
    def setUp(self) -> None:
        _Handler.client_ports = []
        _Handler.requests = []
        _Handler.instructions = "当前服务端规则：按任务所需获取信息。\n保留来源。"
        _Handler.rules = "规则工具返回的协作说明。"
        _Handler.delays = {}
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def test_reuses_one_loopback_connection_without_changing_call_order(self) -> None:
        client = StreamableHTTPClient(f"http://127.0.0.1:{self.server.server_port}/mcp", 5, "token")
        try:
            self.assertEqual(_Handler.instructions, product_instructions(client))
            self.assertEqual(client._connection.sock.getsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY), 1)
            self.assertEqual(client.call_tool("first", {"value": 1}), {"ok": True})
            self.assertEqual(client.call_tool("second", {"value": 2}), {"ok": True})
        finally:
            client.close()
        self.assertEqual([item["method"] for item in _Handler.requests], ["initialize", "notifications/initialized", "tools/call", "tools/call"])
        self.assertEqual(len(set(_Handler.client_ports)), 1)

    def test_batch_budget_does_not_leak_into_queries_on_reused_connection(self) -> None:
        client = StreamableHTTPClient(f"http://127.0.0.1:{self.server.server_port}/mcp", 1)
        try:
            _Handler.delays = {"batch": 0.15, "query": 0.15}
            client.timeout_seconds = 0.5
            self.assertEqual(client.call_tool("batch", {}), {"ok": True})
            client.timeout_seconds = 0.03
            with self.assertRaisesRegex(MCPError, "timed out"):
                client.call_tool("query", {})
        finally:
            client.close()

    def test_reads_product_rules_tool_when_initialization_omits_instructions(self) -> None:
        _Handler.instructions = None
        client = StreamableHTTPClient(f"http://127.0.0.1:{self.server.server_port}/mcp", 5)
        try:
            self.assertEqual(_Handler.rules, product_instructions(client))
        finally:
            client.close()
        self.assertEqual(
            [{"name": "ownward_rules", "arguments": {}}],
            [item["params"] for item in _Handler.requests if item["method"] == "tools/call"],
        )

    def test_rejects_missing_or_malformed_rules_without_a_local_substitute(self) -> None:
        _Handler.instructions = None
        client = StreamableHTTPClient(f"http://127.0.0.1:{self.server.server_port}/mcp", 5)
        try:
            for invalid in (None, "", "  ", {"unexpected": "rules"}):
                with self.subTest(value=invalid):
                    _Handler.rules = invalid
                    with self.assertRaisesRegex(MCPError, "collaboration rules"):
                        product_instructions(client)
        finally:
            client.close()

    def test_rejects_non_loopback_transport(self) -> None:
        with self.assertRaises(MCPError):
            StreamableHTTPClient("https://example.invalid/mcp", 5)


if __name__ == "__main__":
    unittest.main()
