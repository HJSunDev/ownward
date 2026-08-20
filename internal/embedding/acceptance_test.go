package embedding

import (
	"testing"
	"time"
)

func TestTermsAcceptanceIsBoundToExactBundle(t *testing.T) {
	dataRoot := t.TempDir()
	bundle := Bundle{
		Manifest:   Manifest{Legal: LegalArtifacts{AcceptanceID: "legal_first"}},
		LegalPaths: map[string]string{"legal/embeddinggemma/GEMMA_TERMS_OF_USE.md": "terms"},
	}
	status, err := TermsStatus(dataRoot, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if status.Accepted {
		t.Fatal("terms were accepted before explicit confirmation")
	}
	acceptedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	status, err = AcceptTerms(dataRoot, bundle, acceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Accepted || status.AcceptedAt == nil || !status.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("acceptance was not persisted: %#v", status)
	}
	bundle.Manifest.Legal.AcceptanceID = "legal_second"
	status, err = TermsStatus(dataRoot, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if status.Accepted {
		t.Fatal("acceptance for a different legal bundle was reused")
	}
}
