package dnaapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type DNAStatus string

const (
	DNAStatusPending  DNAStatus = "pending"
	DNAStatusApproved DNAStatus = "approved"
	DNAStatusRejected DNAStatus = "rejected"
)

type DNAVersion struct {
	ID          string
	StrategyKey string
	ContentTOML string
	ContentHash string
	DetectedAt  time.Time
}

type DNAApproval struct {
	ID        string
	VersionID string
	Status    DNAStatus
	DecidedBy *string
	DecidedAt *time.Time
	Comment   *string
	CreatedAt time.Time
}

func NewDNAVersion(strategyKey, contentTOML, contentHash string, detectedAt time.Time) (DNAVersion, error) {
	strategyKey = strings.TrimSpace(strategyKey)
	if strategyKey == "" {
		return DNAVersion{}, errors.New("strategy key is required")
	}
	if strings.TrimSpace(contentTOML) == "" {
		return DNAVersion{}, errors.New("content TOML is required")
	}
	if !isValidSHA256Hex(contentHash) {
		return DNAVersion{}, fmt.Errorf("content hash must be sha256 hex, got %q", contentHash)
	}
	if detectedAt.IsZero() {
		return DNAVersion{}, errors.New("detected at is required")
	}

	return DNAVersion{
		ID:          uuid.NewString(),
		StrategyKey: strategyKey,
		ContentTOML: contentTOML,
		ContentHash: strings.ToLower(contentHash),
		DetectedAt:  detectedAt.UTC(),
	}, nil
}

func NewDNAApproval(versionID string, createdAt time.Time) (DNAApproval, error) {
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return DNAApproval{}, errors.New("version id is required")
	}
	if createdAt.IsZero() {
		return DNAApproval{}, errors.New("created at is required")
	}

	return DNAApproval{
		ID:        uuid.NewString(),
		VersionID: versionID,
		Status:    DNAStatusPending,
		CreatedAt: createdAt.UTC(),
	}, nil
}

func isValidSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
