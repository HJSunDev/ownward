from __future__ import annotations

import os
from pathlib import Path
import subprocess
import sys
import unittest

import binding
import report_relationships
import state_relationships


class InternalRelationshipMatrixTests(unittest.TestCase):
    def test_feedback_and_stage_triggers_are_distinct(self) -> None:
        self.assertEqual(["targeted"], state_relationships.plan_for_impacts(["retrieval", "organization"]))
        self.assertEqual(["core"], state_relationships.plan_for_impacts(["asset"]))
        self.assertEqual([], state_relationships.stages_for_impacts(["asset"]))
        self.assertEqual(["core", "frontier", "qualification"], state_relationships.plan_for_stage("kernel-baseline"))

    def test_frontier_has_no_targeted_eligibility_dependency(self) -> None:
        self.assertEqual((), report_relationships.START_ELIGIBILITY["frontier"])
        self.assertEqual(("core", "frontier"), report_relationships.START_ELIGIBILITY["qualification"])
        self.assertEqual(("core", "frontier", "qualification"), state_relationships.BASELINE_AGGREGATES)

    def test_each_internal_scope_accepts_its_minimum_config(self) -> None:
        common = {"schema": "ownward.acceptance-execution/v3", "repository": "repo", "workspace": "workspace", "binding_dir": "binding"}
        frontier = {**common, "enabled_scopes": ["frontier"], "frontier": {"tool": "observer", "targeted_stages": ["lexical"]}}
        core = {**common, "enabled_scopes": ["core"], "candidate": {"binary": "binary", "embedding_bundle_dir": "embedding"}}
        product = {
            **common, "enabled_scopes": ["product"], "candidate": {"binary": "binary", "embedding_bundle_dir": "embedding"},
            "product": {"package": "package", "production_storage_report": "production", "codex_binary": "codex", "codex_auth_file": "auth", "codex_model": "gpt-5.4-mini", "codex_reasoning_effort": "xhigh"},
        }
        for config in (frontier, core, product):
            binding.validate_config(config)

    def test_frontier_environment_excludes_observer_and_embedding(self) -> None:
        first = {"frontier": {"tool": "first"}}
        second = {"frontier": {"tool": "second"}}
        self.assertEqual(binding._environment_manifest(first, "frontier"), binding._environment_manifest(second, "frontier"))

    def test_low_layer_imports_do_not_load_product_or_community_modules(self) -> None:
        suite = Path(__file__).resolve().parent
        script = "import run, preflight, execution; import sys; assert 'product' not in sys.modules; assert 'community' not in sys.modules"
        environment = dict(os.environ)
        completed = subprocess.run([sys.executable, "-c", script], cwd=suite, env=environment, capture_output=True, text=True, timeout=30, check=False)
        self.assertEqual(0, completed.returncode, completed.stderr)


if __name__ == "__main__":
    unittest.main()
