package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

type informationBasis struct {
	Schema   string `json:"v"`
	System   string `json:"s"`
	ID       string `json:"i"`
	Revision uint64 `json:"r"`
	Content  string `json:"c"`
	Notes    string `json:"n"`
	Start    int    `json:"b"`
	End      int    `json:"e"`
}

func (s *Service) source(ctx context.Context, id string) (contract.SourceSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return contract.SourceSnapshot{}, false, err
	}
	a, ok := s.authority.(contract.SourceAuthority)
	if !ok {
		return contract.SourceSnapshot{}, false, errors.New("当前权威不支持一致来源读取")
	}
	return a.ReadSource(strings.TrimSpace(id))
}
func noteDelivery(snapshot contract.SourceSnapshot, start, end int) ([]contract.Clarification, string, error) {
	result := make([]contract.Clarification, 0, len(snapshot.Clarifications))
	fingerprints := make([]string, 0, len(snapshot.Clarifications))
	for _, note := range snapshot.Clarifications {
		v := note.Information
		b, e := 0, utf8.RuneCountInString(v.Content)
		c := contract.Clarification{SourceID: v.ID, SourceRevision: v.Revision}
		if note.Selector != nil {
			var err error
			b, e, err = note.Selector.Resolve(v.Content)
			if err != nil {
				return nil, "", err
			}
			runes := []rune(v.Content)
			unit, err := derived.MaterializeEvidenceUnit(v, derived.EvidenceUnit{Schema: derived.EvidenceUnitSchema, SourceID: v.ID, SourceRevision: v.Revision, StartRune: b, EndRune: e, StartByte: len(string(runes[:b])), EndByte: len(string(runes[:e])), Content: string(runes[b:e])})
			if err != nil {
				return nil, "", err
			}
			ref := unit.Reference()
			c.Evidence = &ref
		}
		c.Covered = v.ID == snapshot.Information.ID && start <= b && end >= e
		data, _ := json.Marshal(struct {
			ID, Hash string
			Selector *domain.TextSelector
		}{v.ID, note.Fingerprint, note.Selector})
		fingerprints = append(fingerprints, string(data))
		result = append(result, c)
	}
	sort.Strings(fingerprints)
	data, _ := json.Marshal(fingerprints)
	hash := sha256.Sum256(data)
	return result, hex.EncodeToString(hash[:]), nil
}
func makeBasis(ctx context.Context, snapshot contract.SourceSnapshot, notes string, start, end int) string {
	v := snapshot.Information
	data, _ := json.Marshal(informationBasis{contract.BasisSchema, contract.InformationSystem(ctx), v.ID, v.Revision, snapshot.Fingerprint, notes, start, end})
	return "b1-" + base64.RawURLEncoding.EncodeToString(data)
}
func (s *Service) ReadInformation(ctx context.Context, id string) (contract.InformationRead, error) {
	snapshot, ok, err := s.source(ctx, id)
	if err != nil {
		return contract.InformationRead{}, err
	}
	if !ok {
		return contract.InformationRead{}, errors.New("信息不存在")
	}
	end := utf8.RuneCountInString(snapshot.Information.Content)
	notes, hash, err := noteDelivery(snapshot, 0, end)
	if err != nil {
		return contract.InformationRead{}, err
	}
	return contract.InformationRead{Information: snapshot.Information, Basis: makeBasis(ctx, snapshot, hash, 0, end), Clarifications: notes}, nil
}
func (s *Service) ReadEvidenceWithBasis(ctx context.Context, id string) (contract.EvidenceRead, error) {
	unit, err := derived.ParseEvidenceUnitID(strings.TrimSpace(id))
	if err != nil {
		return contract.EvidenceRead{}, err
	}
	snapshot, ok, err := s.source(ctx, unit.SourceID)
	if err != nil {
		return contract.EvidenceRead{}, err
	}
	if !ok {
		return contract.EvidenceRead{}, errors.New("信息不存在")
	}
	evidence, err := derived.ResolveEvidence(snapshot.Information, unit)
	if err != nil {
		return contract.EvidenceRead{}, err
	}
	notes, hash, err := noteDelivery(snapshot, unit.StartRune, unit.EndRune)
	if err != nil {
		return contract.EvidenceRead{}, err
	}
	return contract.EvidenceRead{Evidence: evidence, Basis: makeBasis(ctx, snapshot, hash, unit.StartRune, unit.EndRune), Clarifications: notes}, nil
}
func (s *Service) CheckInformation(ctx context.Context, refs []string) ([]contract.InformationCheck, error) {
	if len(refs) > contract.MaxCheckItems {
		return nil, errors.New("每次最多核对 64 项依据，请分批提交")
	}
	results := make([]contract.InformationCheck, 0, len(refs))
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := contract.InformationCheck{Basis: ref, Status: "unverifiable"}
		var b informationBasis
		if len(ref) > 2048 || !strings.HasPrefix(ref, "b1-") {
			results = append(results, r)
			continue
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "b1-"))
		if err != nil || json.Unmarshal(data, &b) != nil || b.Schema != contract.BasisSchema || b.System == "" || b.System != contract.InformationSystem(ctx) || b.ID == "" || b.Revision == 0 || len(b.Content) != 64 || len(b.Notes) != 64 || b.Start < 0 || b.End <= b.Start {
			results = append(results, r)
			continue
		}
		snapshot, ok, err := s.source(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			r.Status = "unavailable"
			results = append(results, r)
			continue
		}
		// 核对不交付原文，不标记说明已读，也不签发新依据。
		notes, hash, err := noteDelivery(snapshot, -1, -1)
		if err != nil {
			return nil, err
		}
		r.Status = "unchanged"
		if b.Revision != snapshot.Information.Revision || b.Content != snapshot.Fingerprint || b.Notes != hash {
			r.Status = "changed"
			r.SourceID = b.ID
			r.Clarifications = notes
		}
		results = append(results, r)
	}
	return results, nil
}
