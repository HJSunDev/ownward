from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import sys
from typing import Any


def _load_evaluator(path: Path) -> Any:
    spec = importlib.util.spec_from_file_location("longmemeval_official_evaluate_qa", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("official evaluator cannot be imported")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    if not callable(getattr(module, "get_anscheck_prompt", None)):
        raise RuntimeError("official evaluator prompt entry is missing")
    return module


def _render(module: Any, request: dict[str, Any]) -> str:
    question = request["question"]
    prompt = module.get_anscheck_prompt(
        question["question_type"],
        question["question"],
        question["answer"],
        request["hypothesis"],
        abstention="_abs" in question["question_id"],
    )
    if not isinstance(prompt, str) or not prompt:
        raise RuntimeError("official evaluator returned no prompt")
    return prompt


def _probe(module: Any, evaluator: Path) -> dict[str, Any]:
    import backoff

    question = {
        "question_id": "noncandidate-evaluator-control",
        "question_type": "single-session-user",
        "question": "Which access color is recorded for the control account?",
        "answer": "cobalt",
    }
    correct = _render(module, {"question": question, "hypothesis": "The recorded access color is cobalt."})
    wrong = _render(module, {"question": question, "hypothesis": "The recorded access color is amber."})
    return {
        "schema": "ownward.kernel-iteration-official-evaluator-probe/v1",
        "python_version": sys.version.split()[0],
        "backoff_version": str(getattr(backoff, "__version__", "unknown")),
        "evaluator_sha256": hashlib.sha256(evaluator.read_bytes()).hexdigest(),
        "correct_prompt_sha256": hashlib.sha256(correct.encode("utf-8")).hexdigest(),
        "wrong_prompt_sha256": hashlib.sha256(wrong.encode("utf-8")).hexdigest(),
        "prompts_distinct": correct != wrong,
    }


def main() -> int:
    if len(sys.argv) not in {2, 3}:
        raise SystemExit("usage: kernel_iteration_official_evaluator_worker.py EVALUATOR [--probe]")
    evaluator = Path(sys.argv[1]).resolve()
    module = _load_evaluator(evaluator)
    if len(sys.argv) == 3:
        if sys.argv[2] != "--probe":
            raise SystemExit("unknown official evaluator worker mode")
        print(json.dumps(_probe(module, evaluator), ensure_ascii=False, sort_keys=True), flush=True)
        return 0

    print(json.dumps({"schema": "ownward.kernel-iteration-official-evaluator-ready/v1", "ready": True}), flush=True)
    for line in sys.stdin:
        request = json.loads(line)
        request_id = request.get("request_id")
        if request.get("action") == "close":
            print(json.dumps({"request_id": request_id, "closed": True}), flush=True)
            return 0
        try:
            prompt = _render(module, request)
            response = {"request_id": request_id, "ok": True, "prompt": prompt}
        except Exception as error:  # The Stage 6 parent treats every worker error as a fail-closed process failure.
            response = {"request_id": request_id, "ok": False, "error_type": type(error).__name__, "message": str(error)}
        print(json.dumps(response, ensure_ascii=False), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
