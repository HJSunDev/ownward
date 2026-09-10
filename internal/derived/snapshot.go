package derived

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/domain"
)

// ExportCurrent copies only the live generation and its current valid records.
// It preserves work identities and vectors and performs no semantic work.
func (s *Store) ExportCurrent(destination string, assets []domain.Information, snapshot, space string) error {
	records, err := s.AllWithEmbeddings()
	if err != nil {
		return err
	}
	live := map[string]uint64{}
	for _, a := range assets {
		live[a.ID] = a.Revision
	}
	kept := records[:0]
	for _, r := range records {
		if live[r.AssetID] == r.AssetRevision {
			kept = append(kept, r)
		}
	}
	generation := s.Generation()
	if generation == "legacy" {
		if err := os.MkdirAll(destination, 0700); err != nil {
			return err
		}
		out, err := Open(destination)
		if err != nil {
			return err
		}
		defer out.Close()
		if len(kept) == 0 {
			return nil
		}
		return out.putRecords(kept, true, false)
	}
	out, err := CreateGeneration(destination, generation)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := out.StageGeneration(kept); err != nil {
		return err
	}
	digest, err := out.seal(GenerationMetadata{AssetCount: len(kept), AssetSnapshot: snapshot, EmbeddingSpace: space})
	if err != nil {
		return err
	}
	data, err := json.Marshal(generationPointer{Schema: currentSchema, Generation: generation, ManifestSHA256: digest})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(destination, currentFileName), data, 0600)
}
