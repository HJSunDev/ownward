package embedding

// CompatibleSpace permits only the allocation-only runtime upgrade verified
// against 39 vectors in bounded-unit-one-20260916.json. Model bytes, text
// preparation, dimensions and floating-point computation remain unchanged.
func CompatibleSpace(source, target string) bool {
	return source == target || source == "emb_79f072bf21c0c0f5226fa4fe6f1946a5" && target == "emb_3ea351940b656cd8366a5581c2758852"
}
