package embedding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const acceptanceSchema = "ownward.embedding-terms-acceptance/v1"

var ErrInvalidTermsAcceptance = errors.New("模型条款确认记录无效")

type AcceptanceStatus struct {
	Accepted     bool              `json:"accepted"`
	RequiredID   string            `json:"required_id"`
	AcceptedAt   *time.Time        `json:"accepted_at,omitempty"`
	LegalFiles   map[string]string `json:"legal_files"`
	AcceptanceAt string            `json:"acceptance_record"`
}

type acceptanceRecord struct {
	Schema       string    `json:"schema"`
	AcceptanceID string    `json:"acceptance_id"`
	AcceptedAt   time.Time `json:"accepted_at"`
}

func TermsStatus(runtimeRoot string, bundle Bundle) (AcceptanceStatus, error) {
	root, err := filepath.Abs(strings.TrimSpace(runtimeRoot))
	if err != nil || strings.TrimSpace(runtimeRoot) == "" {
		return AcceptanceStatus{}, errors.New("运行状态目录无效")
	}
	path := filepath.Join(root, "embedding-terms-acceptance.json")
	status := AcceptanceStatus{
		RequiredID:   bundle.Manifest.Legal.AcceptanceID,
		LegalFiles:   cloneStringMap(bundle.LegalPaths),
		AcceptanceAt: path,
	}
	encoded, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("读取模型条款确认: %w", err)
	}
	var record acceptanceRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return status, fmt.Errorf("%w: %v", ErrInvalidTermsAcceptance, err)
	}
	if record.Schema != acceptanceSchema || strings.TrimSpace(record.AcceptanceID) == "" || record.AcceptedAt.IsZero() {
		return status, ErrInvalidTermsAcceptance
	}
	if record.AcceptanceID != status.RequiredID {
		return status, nil
	}
	status.Accepted = true
	acceptedAt := record.AcceptedAt.UTC()
	status.AcceptedAt = &acceptedAt
	return status, nil
}

func AcceptTerms(runtimeRoot string, bundle Bundle, acceptedAt time.Time) (AcceptanceStatus, error) {
	status, err := TermsStatus(runtimeRoot, bundle)
	if err != nil && !errors.Is(err, ErrInvalidTermsAcceptance) {
		return AcceptanceStatus{}, err
	}
	if status.Accepted {
		return status, nil
	}
	path := status.AcceptanceAt
	if path == "" {
		root, pathErr := filepath.Abs(strings.TrimSpace(runtimeRoot))
		if pathErr != nil || strings.TrimSpace(runtimeRoot) == "" {
			return AcceptanceStatus{}, errors.New("运行状态目录无效")
		}
		path = filepath.Join(root, "embedding-terms-acceptance.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return AcceptanceStatus{}, err
	}
	record := acceptanceRecord{Schema: acceptanceSchema, AcceptanceID: bundle.Manifest.Legal.AcceptanceID, AcceptedAt: acceptedAt.UTC()}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return AcceptanceStatus{}, err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".embedding-terms-*.json")
	if err != nil {
		return AcceptanceStatus{}, err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return AcceptanceStatus{}, err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return AcceptanceStatus{}, err
	}
	if err := temporary.Sync(); err != nil {
		return AcceptanceStatus{}, err
	}
	if err := temporary.Close(); err != nil {
		return AcceptanceStatus{}, err
	}
	if err := replaceAcceptanceFile(temporaryPath, path); err != nil {
		return AcceptanceStatus{}, err
	}
	committed = true
	return TermsStatus(runtimeRoot, bundle)
}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
