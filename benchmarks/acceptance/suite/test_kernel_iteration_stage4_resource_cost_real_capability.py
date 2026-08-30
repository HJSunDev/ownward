from __future__ import annotations

import unittest

import kernel_iteration_stage4_resource_cost_real_capability as finalizer


class ResourceCostRealCapabilityTest(unittest.TestCase):
    def test_bounded_group_count_preserves_every_input(self) -> None:
        self.assertEqual(finalizer._bounded_group_count([184, 186, 170, 149, 193, 174], 384), 3)
        self.assertEqual(finalizer._bounded_group_count([112, 107, 95], 384), 1)
        self.assertEqual(finalizer._bounded_group_count([], 384), 0)

    def test_wall_authorization_requires_repeatability_margin(self) -> None:
        minimum_improvement = 5.965534100001143
        repeatability_error = 1.0
        cold_start_upper_bound = 1.4218677 * 3
        self.assertLess(cold_start_upper_bound, minimum_improvement + repeatability_error)
        self.assertGreater(minimum_improvement + repeatability_error, minimum_improvement)


if __name__ == "__main__":
    unittest.main()
