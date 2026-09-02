from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
from typing import Any
from urllib import request


def _arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Private stdio MCP bridge for one OpenCode worker")
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--callback", required=True)
    parser.add_argument("--token", required=True)
    return parser.parse_args()


def _write(value: dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def _callback(url: str, token: str, name: str, arguments: Any) -> dict[str, Any]:
    body = json.dumps({"name": name, "arguments": arguments}, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    message = request.Request(url, data=body, headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    with request.build_opener(request.ProxyHandler({})).open(message, timeout=300) as response:
        result = json.loads(response.read().decode("utf-8"))
    if not isinstance(result, dict) or set(result) != {"ok", "value", "error"}:
        raise RuntimeError("private MCP callback returned an invalid response")
    return result


def main() -> int:
    arguments = _arguments()
    manifest = json.loads(arguments.manifest.read_text(encoding="utf-8"))
    tools = manifest.get("tools") if isinstance(manifest, dict) else None
    if not isinstance(tools, list):
        return 2
    by_name = {str(item.get("name", "")): item for item in tools if isinstance(item, dict)}
    for line in sys.stdin:
        try:
            message = json.loads(line)
            if not isinstance(message, dict):
                continue
            identifier = message.get("id")
            method = message.get("method")
            if identifier is None:
                continue
            if method == "initialize":
                result = {
                    "protocolVersion": "2025-06-18",
                    "capabilities": {"tools": {"listChanged": False}},
                    "serverInfo": {"name": "ownward-private-tools", "version": "1"},
                }
            elif method == "tools/list":
                result = {"tools": list(by_name.values())}
            elif method == "tools/call":
                params = message.get("params") if isinstance(message.get("params"), dict) else {}
                name = str(params.get("name", ""))
                if name not in by_name:
                    raise RuntimeError(f"tool is not declared: {name}")
                callback = _callback(arguments.callback, arguments.token, name, params.get("arguments"))
                result = {
                    "content": [{
                        "type": "text",
                        "text": json.dumps(callback["value"], ensure_ascii=False, separators=(",", ":")) if callback["ok"] else callback["error"],
                    }],
                    "isError": not callback["ok"],
                }
            elif method == "ping":
                result = {}
            else:
                _write({"jsonrpc": "2.0", "id": identifier, "error": {"code": -32601, "message": "Method not found"}})
                continue
            _write({"jsonrpc": "2.0", "id": identifier, "result": result})
        except Exception as error:
            identifier = message.get("id") if isinstance(locals().get("message"), dict) else None
            if identifier is not None:
                _write({"jsonrpc": "2.0", "id": identifier, "error": {"code": -32000, "message": str(error)}})
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
