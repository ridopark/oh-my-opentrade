package alpaca

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWSClient_ParseBarMessage(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret")
	data := []byte(`{"T": "b", "S": "AAPL", "o": 150.0, "h": 151.0, "l": 149.5, "c": 150.5, "v": 1000, "t": "2024-01-15T10:30:00Z"}`)

	// Act
	bar, err := client.ParseBarMessage(data)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "AAPL", bar.Symbol.String())
	assert.Equal(t, 150.0, bar.Open)
	assert.Equal(t, 151.0, bar.High)
	assert.Equal(t, 149.5, bar.Low)
	assert.Equal(t, 150.5, bar.Close)
	assert.Equal(t, 1000.0, bar.Volume)
	expectedTime, _ := time.Parse(time.RFC3339, "2024-01-15T10:30:00Z")
	assert.Equal(t, expectedTime.UTC(), bar.Time.UTC())
}

func TestWSClient_ParseBarMessage_InvalidJSON(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret")
	data := []byte(`{invalid json`)

	// Act
	_, err := client.ParseBarMessage(data)

	// Assert
	require.Error(t, err)
}

func TestWSClient_ParseBarMessage_FieldMapping(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret")
	data := []byte(`{"T": "b", "S": "MSFT", "o": 300.0, "h": 305.0, "l": 299.0, "c": 302.0, "v": 5000, "t": "2024-01-16T15:00:00Z"}`)

	// Act
	bar, err := client.ParseBarMessage(data)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "MSFT", bar.Symbol.String())
	assert.Equal(t, 300.0, bar.Open)
	assert.Equal(t, 305.0, bar.High)
	assert.Equal(t, 299.0, bar.Low)
	assert.Equal(t, 302.0, bar.Close)
	assert.Equal(t, 5000.0, bar.Volume)
	expectedTime, _ := time.Parse(time.RFC3339, "2024-01-16T15:00:00Z")
	assert.Equal(t, expectedTime.UTC(), bar.Time.UTC())
}

func TestWSClient_Close_Idempotent(t *testing.T) {
	// Arrange
	client := NewWSClient("wss://test", "test-key", "test-secret")

	// Act
	err1 := client.Close()
	err2 := client.Close()

	// Assert
	require.NoError(t, err1)
	require.NoError(t, err2)
}
