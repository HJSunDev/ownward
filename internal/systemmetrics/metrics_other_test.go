//go:build !windows

package systemmetrics

import (
	"testing"
	"time"
)

func TestParsePSCPUTime(t *testing.T) {
	for value, expected := range map[string]time.Duration{
		"01:02":        time.Minute + 2*time.Second,
		"03:04:05":     3*time.Hour + 4*time.Minute + 5*time.Second,
		"2-03:04:05.5": 51*time.Hour + 4*time.Minute + 5500*time.Millisecond,
	} {
		actual, err := parsePSCPUTime(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		if actual != expected {
			t.Fatalf("parse %q = %s, want %s", value, actual, expected)
		}
	}
}
