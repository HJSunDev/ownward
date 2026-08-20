from __future__ import annotations

import json
import subprocess
from pathlib import Path

import numpy as np

from onnx_embedder import OnnxTextEmbedder


ROOT = Path(r"E:\Dev\ownward\.tmp\vector-model-evaluation")
NODE = Path(r"C:\Users\lenovo\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe")
MODELS = (
    ("qwen3_embedding_0_6b", "qwen3-int8"),
    ("embeddinggemma_300m", "embeddinggemma-int8"),
    ("bge_m3", "bge-m3-int8"),
)
TEXT = "如何稳定检索个人信息？"


def main() -> None:
    results = []
    for model_key, directory in MODELS:
        model_dir = ROOT / "models" / directory
        python_embedder = OnnxTextEmbedder(
            ROOT / "state" / "frozen-config.json",
            model_key,
            "deliverable",
            model_dir,
            1,
        )
        python_vector = python_embedder.encode([TEXT], "query")[0]
        process = subprocess.Popen(
            [
                str(NODE),
                str(ROOT / "runtime" / "vector-worker.mjs"),
                str(ROOT / "state" / "frozen-config.json"),
                model_key,
                str(model_dir),
            ],
            cwd=ROOT / "runtime",
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
        )
        if process.stdin is None or process.stdout is None:
            raise RuntimeError("无法建立运行时标准输入输出")
        ready = json.loads(process.stdout.readline())
        if ready.get("type") != "ready":
            raise RuntimeError(f"运行时未就绪：{ready}")
        process.stdin.write(
            json.dumps(
                {"id": "consistency", "text": TEXT, "prompt_type": "query", "return_vector": True},
                ensure_ascii=False,
            )
            + "\n"
        )
        process.stdin.flush()
        response = json.loads(process.stdout.readline())
        node_vector = np.asarray(response.pop("vector"), dtype=np.float32)
        cosine = float(np.dot(python_vector, node_vector))
        maximum_error = float(np.max(np.abs(python_vector - node_vector)))
        process.stdin.write('{"command":"shutdown"}\n')
        process.stdin.flush()
        stopped = json.loads(process.stdout.readline())
        process.wait(timeout=15)
        if stopped.get("type") != "stopped" or process.returncode != 0:
            stderr = process.stderr.read() if process.stderr else ""
            raise RuntimeError(f"运行时未正常停止：{stopped} {process.returncode} {stderr}")
        if cosine < 0.99999:
            raise RuntimeError(f"质量评测与交付运行时向量不一致：{model_key} {cosine}")
        results.append(
            {
                "model_key": model_key,
                "cosine": cosine,
                "maximum_absolute_error": maximum_error,
                "ready": ready,
                "response": response,
                "clean_exit": True,
            }
        )
    output = {"status": "pass", "results": results}
    path = ROOT / "state" / "runtime-consistency.json"
    path.write_text(
        json.dumps(output, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(json.dumps(output, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
