package parity

import (
	"sync"
	"testing"
)

// TestEnabled_ReadsEnvOnce verifies the env var is cached at the first call.
// Note: package-level `once` and `enabled` are reset here via reflection-free
// re-assignment — this test stands alone and doesn't run in parallel with
// other Enabled() callers in the same package.
func TestEnabled_ReadsEnvOnce(t *testing.T) {
	t.Setenv(envVar, "true")

	// Reset the package state for this test.
	once = sync.Once{}
	enabled = false

	if !Enabled() {
		t.Fatal("Enabled() must return true when env=true")
	}

	// Subsequent reads must be cached: changing the env mid-process has no
	// effect (this is intentional — we don't want runtime cardinality bumps
	// from a flipped env var on a long-running process).
	t.Setenv(envVar, "false")
	if !Enabled() {
		t.Fatal("Enabled() must remain true after first call regardless of env changes")
	}
}

func TestEnabled_DefaultOff(t *testing.T) {
	t.Setenv(envVar, "")

	once = sync.Once{}
	enabled = false

	if Enabled() {
		t.Fatal("Enabled() must default to false when env unset")
	}
}

func TestEnabled_NonTrueValue(t *testing.T) {
	t.Setenv(envVar, "1")

	once = sync.Once{}
	enabled = false

	if Enabled() {
		t.Fatal("Enabled() requires the literal 'true' — '1' must not enable")
	}
}
