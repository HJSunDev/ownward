from __future__ import annotations

import json
from pathlib import Path
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
LONGMEM_ROOT = HERE.parents[1] / "longmemeval_s"
sys.path.insert(0, str(LONGMEM_ROOT))

import kernel_iteration_candidate_latency as latency_candidate  # noqa: E402
import kernel_iteration_candidate_resource_cost as resource_candidate  # noqa: E402
import kernel_iteration_evidence as iteration_evidence  # noqa: E402
import semantic_representation  # noqa: E402


class ResourceCostCandidateTests(unittest.TestCase):
    def test_transform_adds_only_quiescent_lossless_compaction(self) -> None:
        repository = HERE.parents[2]
        transform = repository / "manifests/kernel-candidates/v2/resource-cost/collaboration-transform.json"
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "collaboration.go"
            latency_candidate._render_transformed_source(repository, transform, output)
            rendered = output.read_text(encoding="utf-8")
        self.assertIn("remaining, err := s.SemanticWork(ctx, 1)", rendered)
        self.assertIn("s.derivedStore.Compact()", rendered)
        self.assertIn("if len(remaining) == 0", rendered)
        self.assertNotIn("NeedsCompaction", rendered)

    def test_test_overlay_is_not_part_of_runtime_transform(self) -> None:
        repository = HERE.parents[2]
        transform = json.loads((repository / "manifests/kernel-candidates/v2/resource-cost/collaboration-transform.json").read_text(encoding="utf-8"))
        self.assertEqual(transform["source"], "internal/core/collaboration.go")
        self.assertEqual([item["name"] for item in transform["replacements"]], ["compact-derived-log-at-semantic-quiescence"])

    def test_representation_lifecycle_transforms_compose_on_the_real_candidate_sources(self) -> None:
        repository = HERE.parents[2]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stage_service = root / "service-stage.go"
            stage_collaboration = root / "collaboration-stage.go"
            service = root / "service.go"
            collaboration = root / "collaboration.go"
            generation = root / "generation.go"
            lexical = root / "lexical.go"
            latency_candidate._render_transformed_source(
                repository,
                repository / "manifests/kernel-candidates/v2/retrieval-latency/service-transform.json",
                stage_service,
            )
            resource_candidate._render_transformed_input(
                stage_service,
                repository / "manifests/kernel-candidates/v2/resource-cost/representation-service-transform.json",
                service,
            )
            resource_candidate._render_transformed_input(
                service,
                repository / "manifests/kernel-candidates/v2/read-frontier/service-transform.json",
                service,
            )
            latency_candidate._render_transformed_source(
                repository,
                repository / "manifests/kernel-candidates/v2/resource-cost/collaboration-transform.json",
                stage_collaboration,
            )
            resource_candidate._render_transformed_input(
                stage_collaboration,
                repository / "manifests/kernel-candidates/v2/resource-cost/representation-collaboration-transform.json",
                collaboration,
            )
            latency_candidate._render_transformed_source(
                repository,
                repository / "manifests/kernel-candidates/v2/resource-cost/representation-generation-transform.json",
                generation,
            )
            latency_candidate._render_transformed_source(
                repository,
                repository / "manifests/kernel-candidates/v2/read-frontier/lexical-transform.json",
                lexical,
            )
            rendered_service = service.read_text(encoding="utf-8")
            rendered_collaboration = collaboration.read_text(encoding="utf-8")
            rendered_generation = generation.read_text(encoding="utf-8")
            rendered_lexical = lexical.read_text(encoding="utf-8")
        self.assertIn("representations *kernelv2candidate.RepresentationLifecycle", rendered_service)
        self.assertIn("s.ensurePendingRepresentations(ctx)", rendered_service)
        self.assertIn("s.representations.Invalidate(updated.ID, updated.Revision)", rendered_service)
        self.assertIn("lexicalScoreByID", rendered_service)
        self.assertNotIn("s.authority.ReadCurrent, len(sourceIDs)", rendered_service)
        self.assertIn("kernelv2candidate.FixedSourceOrder", rendered_service)
        self.assertIn("s.index.SourceMetadata", rendered_service)
        self.assertIn("func (l *Lexical) SourceMetadata", rendered_lexical)
        self.assertIn("diversity   coverage.Sketch", rendered_lexical)
        self.assertNotIn("func (l *Lexical) SourceMetadata", (repository / "internal/retrieval/lexical.go").read_text(encoding="utf-8"))
        self.assertIn("s.representations.Ensure(ctx, asset)", rendered_collaboration)
        self.assertEqual(rendered_collaboration.count("s.representations.Commit("), 3)
        self.assertNotIn("s.embedder.EmbedDocuments(ctx, contents[offset:end])", rendered_collaboration)
        self.assertIn("generationCandidates(asset, nil", rendered_generation)

    def test_semantic_representation_manifest_is_content_addressed_and_composition_declared(self) -> None:
        repository = HERE.parents[2]
        path = repository / "manifests/kernel-candidates/v2/resource-cost/semantic-representation.json"
        contract = semantic_representation.load_contract(path)
        value = json.loads(path.read_text(encoding="utf-8"))
        self.assertEqual(contract.representation, semantic_representation.COMPACT_REPRESENTATION)
        self.assertEqual(value["selection"], "candidate-composition-declared")
        self.assertTrue(value["formal_requires_bound_candidate"])

    def test_candidate_access_transform_binds_bounded_and_complete_source_context_to_current_revision(self) -> None:
        repository = HERE.parents[2]
        transform = repository / "manifests/kernel-candidates/v2/final-answer-sufficiency/mcpserver-transform.json"
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "server.go"
            latency_candidate._render_transformed_source(repository, transform, output)
            rendered = output.read_text(encoding="utf-8")
        self.assertIn('SourcePreludeStartRune', rendered)
        self.assertIn('json:"source_prelude_start_rune"', rendered)
        self.assertIn('SourceCompleteStartRune', rendered)
        self.assertIn('json:"source_complete_start_rune"', rendered)
        self.assertIn("probeLimit := min(limit+1, 8)", rendered)
        self.assertIn("result.SourceRunes = utf8.RuneCountInString(source.Content)", rendered)
        self.assertIn("source.Revision != value.SourceRevision", rendered)
        self.assertIn("endRune := min(128, value.StartRune)", rendered)
        self.assertIn("result.SourcePrelude = source.Content[:endByte]", rendered)
        self.assertNotIn("candidateEvidenceRead", (repository / "internal/adapter/mcpserver/server.go").read_text(encoding="utf-8"))

    def test_context_sufficiency_contract_binds_root_reproduction_and_confirmation(self) -> None:
        path = HERE / "iteration/v2/stage3-context-sufficiency-contract.json"
        contract = json.loads(path.read_text(encoding="utf-8"))
        identity = contract.pop("identity")
        self.assertEqual(identity, iteration_evidence.canonical_sha256(contract))
        self.assertTrue(contract["frozen_before_candidate_results"])
        self.assertEqual(contract["gates"]["confirmation_cases"], 3)
        for source in contract["sources"].values():
            source_path = HERE / source["path"]
            self.assertEqual(source["sha256"], iteration_evidence.file_sha256(source_path))
        self.assertTrue(contract["candidate_constraints"]["no_blind_or_formal_execution"])


if __name__ == "__main__":
    unittest.main()
