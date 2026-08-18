package acceptance

import "testing"

func TestRelationKeyTreatsRelatedToAsSymmetric(t *testing.T) {
	forward := relationKey("I018", "related_to", "I019")
	reverse := relationKey("I019", "related_to", "I018")
	if forward != reverse {
		t.Fatalf("related_to must be symmetric: %q != %q", forward, reverse)
	}
	if relationKey("I018", "supports", "I019") == relationKey("I019", "supports", "I018") {
		t.Fatal("directional relations must preserve direction")
	}
}
