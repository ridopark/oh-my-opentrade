package coinbase

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/config"
)

func TestNewClient_AppliesDefaults(t *testing.T) {
	c := NewClient(config.CoinbaseConfig{}, zerolog.Nop())
	require.NotNil(t, c)
	assert.Equal(t, defaultBaseURL, c.baseURL)
	assert.NotNil(t, c.limiter)
	require.NotNil(t, c.httpClient)
	assert.Equal(t, time.Duration(defaultTimeoutSec)*time.Second, c.httpClient.Timeout)
}

func TestNewClient_HonorsConfig(t *testing.T) {
	cfg := config.CoinbaseConfig{
		BaseURL:        "https://example.test",
		RateLimitRPS:   4,
		TimeoutSeconds: 7,
	}
	c := NewClient(cfg, zerolog.Nop())
	assert.Equal(t, "https://example.test", c.baseURL)
	assert.Equal(t, 7*time.Second, c.httpClient.Timeout)
}

func TestParseRetryAfter(t *testing.T) {
	assert.Equal(t, time.Duration(0), parseRetryAfter(""))
	assert.Equal(t, 3*time.Second, parseRetryAfter("3"))
	// HTTP-date in the past should return 0
	assert.Equal(t, time.Duration(0), parseRetryAfter("Mon, 01 Jan 1990 00:00:00 GMT"))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 10))
	assert.Equal(t, "abc...", truncate("abcdef", 3))
}
