from __future__ import annotations

import unittest

import kernel_iteration_stage4_resource_cost_create_probe as probe


class ResourceCostCreateProbeTests(unittest.TestCase):
    def test_observer_transform_preserves_work_and_adds_required_trace(self) -> None:
        service = '''package core
import (
\t"context"
\t"encoding/hex"
\t"math"
\t"sync"
)
const CollaborationRules = productrules.Collaboration
func (s *Service) CreateBatch(ctx context.Context, inputs []CreateInput) ([]MutationBatchResult, error) {
\tif len(inputs) == 0 || len(inputs) > 20 {
\t\treturn nil, errors.New("x")
\t}
\tif len(values) > 0 {
\t\tif _, err := s.authority.CreateAssets(values); err != nil {
\t\t\tfor _, position := range positions {
\t\t\t\tresults[position].Error = err.Error()
\t\t\t}
\t\t\treturn results, nil
\t\t}
\t\tfor _, value := range values {
\t\t\ts.index.Upsert(value)
\t\t}
\t\tif s.evidencePlans != nil {
\t\t\ts.evidencePlans.Reset()
\t\t}
\t}
}'''
        transformed = probe._instrument_service(service)
        self.assertIn('"phase": "create.envelope"', transformed)
        self.assertIn('"phase": "authority.create_batch"', transformed)
        self.assertIn('"phase": "lexical.memory_index"', transformed)

    def test_route_gate_includes_repeatability_margin(self) -> None:
        minimum = 5.965534100001143
        error = 0.9999999999990905
        self.assertAlmostEqual(minimum + error, 6.965534100000234)
        self.assertLess(4.2656031, minimum + error)

    def test_batch_transform_preserves_per_item_bound_and_input_limit(self) -> None:
        source = '''func boundedEmbeddingBatchEnd(values []string, start int) int {
\tend, total := start, 0
\tfor end < len(values) && end-start < 32 {
\t\tsize := len([]byte(values[end]))
\t\tif end > start && total+size > semanticEmbeddingChunkBytes {
\t\t\tbreak
\t\t}
\t\ttotal += size
\t\tend++
\t}
\treturn end
}'''
        transformed = probe._batch_independent_short_documents(source)
        self.assertIn("end-start < 32", transformed)
        self.assertIn("len([]byte(values[end])) > semanticEmbeddingChunkBytes", transformed)
        self.assertIn("if end == start", transformed)
        self.assertNotIn("total+size", transformed)


if __name__ == "__main__":
    unittest.main()
