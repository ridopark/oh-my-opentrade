package dnaapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewDNAVersion_Validation(t *testing.T) {
	t.Run("requires strategy key", func(t *testing.T) {
		_, err := NewDNAVersion("", "[x]\na=1\n", sha256Hex("x"), time.Now())
		require.Error(t, err)
	})

	t.Run("requires content", func(t *testing.T) {
		_, err := NewDNAVersion("orb", " ", sha256Hex("x"), time.Now())
		require.Error(t, err)
	})

	t.Run("requires sha256 hex", func(t *testing.T) {
		_, err := NewDNAVersion("orb", "[x]\na=1\n", "not-a-hash", time.Now())
		require.Error(t, err)
	})

	t.Run("requires detectedAt", func(t *testing.T) {
		_, err := NewDNAVersion("orb", "[x]\na=1\n", sha256Hex("x"), time.Time{})
		require.Error(t, err)
	})
}

func TestNewDNAVersion_Success(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 3600))
	v, err := NewDNAVersion(" orb ", "[strategy]\nid='orb'\n", sha256Hex("abc"), now)
	require.NoError(t, err)
	require.NotEmpty(t, v.ID)
	require.Equal(t, "orb", v.StrategyKey)
	require.Equal(t, "[strategy]\nid='orb'\n", v.ContentTOML)
	require.Equal(t, sha256Hex("abc"), v.ContentHash)
	require.True(t, v.DetectedAt.Location() == time.UTC)
}

func TestNewDNAApproval_Validation(t *testing.T) {
	_, err := NewDNAApproval("", time.Now())
	require.Error(t, err)

	_, err = NewDNAApproval("ver-1", time.Time{})
	require.Error(t, err)
}

func TestNewDNAApproval_Success(t *testing.T) {
	createdAt := time.Now()
	a, err := NewDNAApproval("ver-1", createdAt)
	require.NoError(t, err)
	require.NotEmpty(t, a.ID)
	require.Equal(t, "ver-1", a.VersionID)
	require.Equal(t, DNAStatusPending, a.Status)
	require.Nil(t, a.DecidedBy)
	require.Nil(t, a.DecidedAt)
	require.Nil(t, a.Comment)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
