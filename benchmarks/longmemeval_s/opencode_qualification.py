from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys
import time
from typing import Any, Callable


BENCHMARK_ROOT = Path(__file__).resolve().parent
SUPPORT_ROOT = BENCHMARK_ROOT.parent / "support"
SUITE_ROOT = BENCHMARK_ROOT.parent / "acceptance" / "suite"
VALIDATION_CONTRACT_PATH = SUITE_ROOT / "iteration" / "v2" / "validation-contract.json"
# Preserve the caller's import precedence.  Both the LongMemEval adapter and
# Acceptance CLI are named ``run.py``; prepending the suite directory here
# would make importing this helper redirect later ``import run`` calls to the
# wrong program.
for dependency in (BENCHMARK_ROOT, SUPPORT_ROOT, SUITE_ROOT):
    if str(dependency) not in sys.path:
        sys.path.append(str(dependency))

from external_intelligence import (  # noqa: E402
    ExternalIntelligenceError,
    ExternalIntelligenceExecutor,
    InvocationLifecycle,
    canonical_sha256,
)
from external_intelligence_runtime import current_runtime_identity, open_external_intelligence_runtime  # noqa: E402
import kernel_iteration_validation as validation_runtime  # noqa: E402
import kernel_iteration_blind_suite as blind_suite_runtime  # noqa: E402


DRIVER = "opencode-server/v1"
PROVIDER = "opencode-go"
MODEL = "qwen3.8-flash"
SCHEMA = "ownward.external-intelligence-role-qualification/v3"
ROLE_ORDER = ("generator", "quality-admission", "semantic-organization", "reader", "judge")
ROLE_EFFORT_ORDER = {
    "generator": ("xhigh",),
    "quality-admission": ("xhigh",),
    "semantic-organization": ("xhigh",),
    "reader": ("xhigh",),
    "judge": ("medium",),
}


def _atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def _load_validation_contract() -> dict[str, Any]:
    value = json.loads(VALIDATION_CONTRACT_PATH.read_text(encoding="utf-8"))
    content = {key: item for key, item in value.items() if key != "identity"}
    if value.get("identity") != canonical_sha256(content):
        raise ExternalIntelligenceError("qualification validation contract identity changed")
    return value


class _RetrievalFixture:
    def __init__(self) -> None:
        self.calls: list[dict[str, Any]] = []
        self.searched = False

    def reset(self) -> None:
        self.calls = []
        self.searched = False

    def call(self, name: str, arguments: Any) -> Any:
        if name == "ownward_search":
            self.searched = True
            result = {"results": [{"id": "qualification-fact", "summary": "A synthetic harbor record is available."}]}
        elif name == "ownward_read" and self.searched and isinstance(arguments, dict) and arguments.get("id") == "qualification-fact":
            result = {"information": {"id": "qualification-fact", "content": "The synthetic harbor marker is cedar-731."}}
        else:
            raise ValueError("qualification tool call is invalid")
        self.calls.append({"tool": name, "arguments": arguments, "result": result})
        return result

    def validate(self) -> None:
        if [item["tool"] for item in self.calls] != ["ownward_search", "ownward_read"]:
            raise ValueError("qualification reader did not complete the required search/read sequence")

    def report(self) -> dict[str, Any]:
        return {"tools": [item["tool"] for item in self.calls]}

    def restore(self, value: Any) -> None:
        tools = value.get("tools") if isinstance(value, dict) else None
        if tools == ["ownward_search", "ownward_read"]:
            self.calls = [{"tool": name} for name in tools]
            self.searched = True


TOOLS = [
    {
        "type": "function", "name": "ownward_search", "description": "Search the synthetic Ownward qualification source.",
        "inputSchema": {"type": "object", "additionalProperties": False, "required": ["query"], "properties": {"query": {"type": "string"}}},
        "deferLoading": False,
    },
    {
        "type": "function", "name": "ownward_read", "description": "Read an ID returned by synthetic Ownward search.",
        "inputSchema": {"type": "object", "additionalProperties": False, "required": ["id"], "properties": {"id": {"type": "string"}}},
        "deferLoading": False,
    },
]


RoleInvocation = Callable[[str], dict[str, Any]]


def _select_role_effort(role: str, invoke: RoleInvocation) -> tuple[str, dict[str, Any], dict[str, str]]:
    failures: dict[str, str] = {}
    effort_order = ROLE_EFFORT_ORDER.get(role)
    if effort_order is None:
        raise ExternalIntelligenceError(f"unknown qualification role: {role}")
    for effort in effort_order:
        try:
            result = invoke(effort)
        except (ExternalIntelligenceError, OSError, ValueError) as error:
            failures[effort] = str(error)
            if effort == effort_order[-1]:
                raise ExternalIntelligenceError(
                    f"{role} failed qualification at {', '.join(effort_order)}: {error}"
                ) from error
        else:
            return effort, result, failures
    raise ExternalIntelligenceError(f"{role} exhausted qualification efforts")


def _invoke_generator(
    executor: ExternalIntelligenceExecutor,
    stage: Path,
    effort: str,
    validation: dict[str, Any],
) -> dict[str, Any]:
    case_id = "b01-c01"
    coverage = "knowledge-update-conflict"
    output, usage = executor.invoke(
        role="generator",
        prompt=validation_runtime._generator_prompt(  # noqa: SLF001 - qualification intentionally uses the production contract
            validation, "opencode-go-qwen3.8-flash-role-qualification-v1", case_id, coverage,
        ),
        schema=validation_runtime._generator_case_schema(case_id, coverage, validation),  # noqa: SLF001
        stage=stage,
        model=MODEL,
        effort=effort,
        timeout_seconds=240,
        attempts=2,
        validate=lambda value: validation_runtime._validate_generated_case(value, case_id, coverage, validation),  # noqa: SLF001
    )
    generated = validation_runtime._validate_generated_case(output, case_id, coverage, validation)  # noqa: SLF001
    return {
        "value": generated,
        "usage": usage,
        "evidence": {
            "case_identity": canonical_sha256({key: value for key, value in generated.items() if not key.startswith("_")}),
            "coverage": coverage,
            "sessions": len(generated["sessions"]),
            "answer_sources": len(generated["answer_session_ids"]),
            "mechanical_validation": "passed",
        },
    }


def _invoke_admission(
    executor: ExternalIntelligenceExecutor,
    stage: Path,
    effort: str,
    validation: dict[str, Any],
    generated: dict[str, Any],
) -> dict[str, Any]:
    case = {
        **{key: value for key, value in generated.items() if key != "_mechanical_admission_proof"},
        "mechanical_admission_proof": generated.get("_mechanical_admission_proof"),
    }
    controls, expected = blind_suite_runtime._qualification_controls()  # noqa: SLF001 - reuse the production admission controls
    review = blind_suite_runtime._qualification_materials([case, *controls])  # noqa: SLF001
    required = validation["blind"]["quality_admission"]["required_checks"]
    positive_ids = [str(case["case_id"]), *expected["positive_case_ids"]]

    def validate(value: dict[str, Any]) -> None:
        validation_runtime.validate_admission(value, review, validation)
        by_id = {str(item.get("case_id")): item.get("checks") for item in value.get("assessments", []) if isinstance(item, dict)}
        positives_passed = all(
            isinstance(by_id.get(case_id), dict) and all(by_id[case_id].get(check) is True for check in required)
            for case_id in positive_ids
        )
        negatives_passed = all(
            isinstance(by_id.get(case_id), dict) and by_id[case_id].get(check) is False
            for case_id, check in expected["negative_target_checks"].items()
        )
        if not positives_passed or not negatives_passed:
            raise ValueError("quality admission did not distinguish the fixed positive and negative controls")

    output, usage = executor.invoke(
        role="quality-admission",
        prompt=validation_runtime._admission_prompt(validation, review),  # noqa: SLF001
        schema=validation_runtime._admission_schema([str(item["case_id"]) for item in review["cases"]]),  # noqa: SLF001
        stage=stage,
        model=MODEL,
        effort=effort,
        timeout_seconds=240,
        attempts=2,
        validate=validate,
    )
    validate(output)
    return {
        "value": output,
        "usage": usage,
        "evidence": {
            "generated_case_identity": canonical_sha256(case),
            "required_checks": len(required),
            "positive_controls_passed": len(positive_ids),
            "negative_controls_passed": len(expected["negative_target_checks"]),
        },
    }


def _invoke_semantic(executor: ExternalIntelligenceExecutor, stage: Path, effort: str) -> dict[str, Any]:
    output, usage = executor.invoke(
        role="semantic-organization",
        prompt=(
            "Act as Ownward's external semantic capability. Analyze every synthetic work item exactly once, preserve "
            "identity and order, do not invent relationships, and return concise summaries, topics, and durable cues. "
            'Input: [{"work_id":"work-a","asset_id":"asset-a","revision":3,"content":"Mira selected the copper route."},'
            '{"work_id":"work-b","asset_id":"asset-b","revision":7,"content":"Later Mira replaced it with the cedar route."}]'
        ),
        schema={
            "type": "object", "additionalProperties": False, "required": ["analyses"],
            "properties": {"analyses": {"type": "array", "minItems": 2, "maxItems": 2, "items": {
                "type": "object", "additionalProperties": False, "required": ["work_id", "summary", "topics", "cues"],
                "properties": {
                    "work_id": {"type": "string", "enum": ["work-a", "work-b"]},
                    "summary": {"type": "string", "minLength": 1},
                    "topics": {"type": "array", "maxItems": 4, "items": {"type": "string"}},
                    "cues": {"type": "array", "maxItems": 4, "items": {
                        "type": "object", "additionalProperties": False, "required": ["text", "kind"],
                        "properties": {"text": {"type": "string", "minLength": 1}, "kind": {"type": "string", "minLength": 1}},
                    }},
                },
            }}},
        },
        stage=stage,
        model=MODEL,
        effort=effort,
        timeout_seconds=180,
        attempts=2,
        validate=lambda value: (
            None if [item.get("work_id") for item in value.get("analyses", [])] == ["work-a", "work-b"]
            else (_ for _ in ()).throw(ValueError("semantic qualification changed work identity or order"))
        ),
    )
    return {
        "value": output,
        "usage": usage,
        "evidence": {"work_items": 2, "identity_and_order": "preserved", "structured_analysis": "passed"},
    }


def _invoke_reader(executor: ExternalIntelligenceExecutor, stage: Path, effort: str) -> dict[str, Any]:
    retrieval = _RetrievalFixture()
    output, usage = executor.invoke(
        role="reader",
        prompt="Use Ownward search, read the returned record, and return the exact synthetic harbor marker.",
        schema={
            "type": "object", "additionalProperties": False, "required": ["answer"],
            "properties": {"answer": {"type": "string", "enum": ["cedar-731"]}},
        },
        stage=stage,
        model=MODEL,
        effort=effort,
        timeout_seconds=180,
        attempts=2,
        lifecycle=InvocationLifecycle(
            retrieval_mode="qualification-progressive/v1",
            tool_manifest_identity=canonical_sha256(TOOLS),
            dynamic_tools=TOOLS,
            tool_handler=retrieval.call,
            base_instructions="Use only the supplied synthetic Ownward tools; search before reading.",
            reset_attempt=retrieval.reset,
            restore=retrieval.restore,
            validate=retrieval.validate,
            report=retrieval.report,
        ),
    )
    retrieval.validate()
    return {"value": output, "usage": usage, "evidence": {"tool_sequence": retrieval.report()["tools"], "answer": "exact"}}


def _invoke_judge(executor: ExternalIntelligenceExecutor, stage: Path, effort: str) -> dict[str, Any]:
    expected = {"matching": "yes", "conflicting": "no"}

    def validate(value: dict[str, Any]) -> None:
        decisions = value.get("decisions")
        actual = {item.get("case_id"): item.get("label") for item in decisions if isinstance(item, dict)} if isinstance(decisions, list) else {}
        if actual != expected:
            raise ValueError("judge qualification did not distinguish matching and conflicting answers")

    output, usage = executor.invoke(
        role="judge",
        prompt=(
            "Judge both synthetic answer pairs independently. Return yes only when the prediction conveys the same fact "
            "as the reference. matching: reference cedar-731, prediction cedar-731. conflicting: reference cedar-731, prediction copper-204."
        ),
        schema={
            "type": "object", "additionalProperties": False, "required": ["decisions"],
            "properties": {"decisions": {"type": "array", "minItems": 2, "maxItems": 2, "items": {
                "type": "object", "additionalProperties": False, "required": ["case_id", "label"],
                "properties": {
                    "case_id": {"type": "string", "enum": ["matching", "conflicting"]},
                    "label": {"type": "string", "enum": ["yes", "no"]},
                },
            }}},
        },
        stage=stage,
        model=MODEL,
        effort=effort,
        timeout_seconds=180,
        attempts=2,
        validate=validate,
    )
    validate(output)
    return {"value": output, "usage": usage, "evidence": {"matching": "accepted", "conflicting": "rejected"}}


def _qualification_identity(runtime_identity: str, validation: dict[str, Any]) -> str:
    return canonical_sha256({
        "schema": SCHEMA,
        "driver": DRIVER,
        "provider": PROVIDER,
        "model": MODEL,
        "role_effort_order": ROLE_EFFORT_ORDER,
        "roles": ROLE_ORDER,
        "runtime_identity": runtime_identity,
        "validation_contract_identity": validation["identity"],
        "controller_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "production_validation_sha256": hashlib.sha256(Path(validation_runtime.__file__).read_bytes()).hexdigest(),
        "production_admission_controls_sha256": hashlib.sha256(Path(blind_suite_runtime.__file__).read_bytes()).hexdigest(),
    })


def _load_terminal_selection(path: Path, identity: str) -> dict[str, Any] | None:
    if not path.is_file():
        return None
    value = json.loads(path.read_text(encoding="utf-8"))
    content = {key: item for key, item in value.items() if key != "selection_identity"}
    if value.get("schema") != SCHEMA or value.get("selection_identity") != canonical_sha256(content) or value.get("passed") is not True:
        raise ExternalIntelligenceError("external-intelligence qualification selection identity changed")
    if value.get("qualification_identity") != identity:
        audit = path.parent / "_audit"
        audit.mkdir(parents=True, exist_ok=True)
        archived = audit / f"selection-{value.get('qualification_identity', 'unknown')}.json"
        if archived.is_file():
            if archived.read_bytes() != path.read_bytes():
                raise ExternalIntelligenceError("external-intelligence qualification audit identity changed")
            path.unlink()
        else:
            path.replace(archived)
        return None
    return value


def qualify(binary: Path, credential_file: Path, output_dir: Path) -> dict[str, Any]:
    binary = binary.resolve()
    credential_file = credential_file.resolve()
    output_dir = output_dir.resolve()
    validation = _load_validation_contract()
    runtime_identity = current_runtime_identity(
        driver=DRIVER, binary=binary, credential_file=credential_file, max_active=1, worker_processes=1,
    )
    qualification_identity = _qualification_identity(runtime_identity, validation)
    selection_path = output_dir / "selection.json"
    terminal = _load_terminal_selection(selection_path, qualification_identity)
    if terminal is not None:
        return {**terminal, "reused": True, "current_run_external_calls": 0}

    started = time.perf_counter()
    role_results: dict[str, Any] = {}
    total_calls = 0
    generation_root = output_dir / "generations" / qualification_identity
    with open_external_intelligence_runtime(
        driver=DRIVER,
        binary=binary,
        credential_file=credential_file,
        max_active=1,
        worker_processes=1,
        runtime_parent=generation_root / ".runtime",
    ) as transport:
        executor = ExternalIntelligenceExecutor(transport)

        def qualify_role(role: str, callback: Callable[[str], dict[str, Any]]) -> dict[str, Any]:
            nonlocal total_calls

            def invoke(effort: str) -> dict[str, Any]:
                nonlocal total_calls
                total_calls += 1
                return callback(effort)

            effort, result, failures = _select_role_effort(role, invoke)
            return {
                "passed": True,
                "reasoning_effort": effort,
                "prior_failures": failures,
                "usage": result["usage"],
                "evidence": result["evidence"],
                "value": result["value"],
            }

        role_results["generator"] = qualify_role(
            "generator", lambda effort: _invoke_generator(executor, generation_root / "roles" / "generator" / effort, effort, validation),
        )
        generated = role_results["generator"]["value"]
        role_results["quality-admission"] = qualify_role(
            "quality-admission",
            lambda effort: _invoke_admission(executor, generation_root / "roles" / "quality-admission" / effort, effort, validation, generated),
        )
        role_results["semantic-organization"] = qualify_role(
            "semantic-organization", lambda effort: _invoke_semantic(executor, generation_root / "roles" / "semantic-organization" / effort, effort),
        )
        role_results["reader"] = qualify_role(
            "reader", lambda effort: _invoke_reader(executor, generation_root / "roles" / "reader" / effort, effort),
        )
        role_results["judge"] = qualify_role(
            "judge", lambda effort: _invoke_judge(executor, generation_root / "roles" / "judge" / effort, effort),
        )
        diagnostics = transport.diagnostics()

    public_roles = {
        role: {key: item for key, item in result.items() if key != "value"}
        for role, result in role_results.items()
    }
    content = {
        "schema": SCHEMA,
        "qualification_identity": qualification_identity,
        "passed": all(result["passed"] is True for result in public_roles.values()),
        "driver": DRIVER,
        "provider": PROVIDER,
        "model": MODEL,
        "selection_rule": "qualified-fixed-profile:intelligence-heavy-xhigh;judge-medium",
        "validation_contract_identity": validation["identity"],
        "runtime_identity": runtime_identity,
        "roles": public_roles,
        "transport": diagnostics,
        "wall_seconds": time.perf_counter() - started,
    }
    selected = {**content, "selection_identity": canonical_sha256(content)}
    if not selected["passed"]:
        raise ExternalIntelligenceError("external-intelligence qualification did not satisfy every role")
    _atomic_json(selection_path, selected)
    return {**selected, "reused": False, "current_run_external_calls": total_calls}


def main() -> int:
    parser = argparse.ArgumentParser(description="Qualify every OpenCode Go + Qwen3.8 Flash role without formal evidence state")
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--credential-file", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    arguments = parser.parse_args()
    try:
        result = qualify(arguments.binary, arguments.credential_file, arguments.output_dir)
    except (ExternalIntelligenceError, OSError, ValueError) as error:
        print(f"qualification failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
