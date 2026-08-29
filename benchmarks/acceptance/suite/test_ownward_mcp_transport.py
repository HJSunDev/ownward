from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import sys
import threading
import unittest


SUPPORT = Path(__file__).resolve().parents[2] / "support"
sys.path.insert(0, str(SUPPORT))

from ownward_mcp import MCPError, StreamableHTTPClient  # noqa: E402


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    client_ports: list[int] = []
    requests: list[dict] = []

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        payload = json.loads(self.rfile.read(length))
        type(self).client_ports.append(self.client_address[1])
        type(self).requests.append(payload)
        if payload.get("method") == "notifications/initialized":
            self._write(202, b"")
            return
        if payload.get("method") == "initialize":
            result = {"protocolVersion": "2025-06-18", "capabilities": {}, "serverInfo": {"name": "test", "version": "1"}}
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
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        return


class StreamableHTTPClientTests(unittest.TestCase):
    def setUp(self) -> None:
        _Handler.client_ports = []
        _Handler.requests = []
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
            self.assertEqual(client._connection.sock.getsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY), 1)
            self.assertEqual(client.call_tool("first", {"value": 1}), {"ok": True})
            self.assertEqual(client.call_tool("second", {"value": 2}), {"ok": True})
        finally:
            client.close()
        self.assertEqual([item["method"] for item in _Handler.requests], ["initialize", "notifications/initialized", "tools/call", "tools/call"])
        self.assertEqual(len(set(_Handler.client_ports)), 1)

    def test_rejects_non_loopback_transport(self) -> None:
        with self.assertRaises(MCPError):
            StreamableHTTPClient("https://example.invalid/mcp", 5)


if __name__ == "__main__":
    unittest.main()
