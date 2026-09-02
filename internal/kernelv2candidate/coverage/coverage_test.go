package coverage

import "testing"

func TestDistanceIsStableAndBounded(t *testing.T) {
	var left, right Sketch
	left.Add("shared")
	left.Add("left")
	right.Add("shared")
	right.Add("right")
	if got := Distance(left, left); got != 0 {
		t.Fatalf("identical signature distance = %v", got)
	}
	if got := Distance(left, right); got <= 0 || got > 1 {
		t.Fatalf("distinct signature distance out of range: %v", got)
	}
}

func TestFromTextIsLanguageIndependentAndDeterministic(t *testing.T) {
	left := FromText("Harbor relay 状态确认")
	right := FromText("harbor relay 状态确认")
	if left != right || left.Count == 0 {
		t.Fatalf("bounded summary sketch drifted: left=%+v right=%+v", left, right)
	}
	if Distance(left, FromText("botanical archive 植物档案")) == 0 {
		t.Fatal("distinct multilingual summaries collapsed to one sketch")
	}
}
