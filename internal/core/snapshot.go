package core

import "path/filepath"

// WithSnapshot waits for accepted local work, then captures a consistent image.
// The caller freezes the authority first; no network or model runs under this lock.
func (s *Service) ExportSnapshot(destination string, exportAuthority func() error) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := exportAuthority(); err != nil {
		return err
	}
	if s.derivedStore == nil {
		return nil
	}
	assets := s.authority.ListCurrent()
	snapshot, err := informationSnapshotDigest(assets)
	if err != nil {
		return err
	}
	space := ""
	if s.embedder != nil {
		space = s.embedder.Space().ID
	}
	return s.derivedStore.ExportCurrent(filepath.Join(destination, "state"), assets, snapshot, space)
}
