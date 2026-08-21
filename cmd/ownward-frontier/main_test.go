package main

import (
	"math"
	"testing"
)

func TestMetricsUseRepeatedMeasurements(t *testing.T) {
	runs := []collector{
		{"query_p95_ms": &sampleSpec{Dimension: "latency", Stage: "fusion", Direction: "lower", Values: []float64{9, 10}}},
		{"query_p95_ms": &sampleSpec{Dimension: "latency", Stage: "fusion", Direction: "lower", Values: []float64{10, 11}}},
		{"query_p95_ms": &sampleSpec{Dimension: "latency", Stage: "fusion", Direction: "lower", Values: []float64{11, 12}}},
	}
	metrics, err := metricsFromRuns(runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}
	metric := metrics[0]
	if math.Abs(metric.Value-11) > 1e-9 || math.Abs(metric.RepeatabilityError-1) > 1e-9 {
		t.Fatalf("unexpected measured result: %#v", metric)
	}
	if metric.Materiality < 2*metric.RepeatabilityError {
		t.Fatalf("materiality must exceed measured noise: %#v", metric)
	}
}

func TestMetricsRejectInconsistentRepeatedMeasurements(t *testing.T) {
	runs := []collector{
		{"recall": &sampleSpec{Dimension: "quality", Stage: "fusion", Direction: "higher", Values: []float64{1}}},
		{},
	}
	if _, err := metricsFromRuns(runs); err == nil {
		t.Fatal("expected inconsistent repeated measurements to fail")
	}
}
