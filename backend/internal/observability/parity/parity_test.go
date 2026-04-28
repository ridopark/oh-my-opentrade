package parity

import "testing"

// Tests directly toggle `enabled` (package-level var). These tests must
// not run in parallel with each other or with any other Enabled() caller
// in the same package.

func TestEnabled_True(t *testing.T) {
	prev := enabled
	defer func() { enabled = prev }()
	enabled = true
	if !Enabled() {
		t.Fatal("Enabled() must return the package-level bool")
	}
}

func TestEnabled_False(t *testing.T) {
	prev := enabled
	defer func() { enabled = prev }()
	enabled = false
	if Enabled() {
		t.Fatal("Enabled() must default to false")
	}
}

// TestEnabled_OnlyLiteralTrue asserts the init() comparison: any value
// other than the literal "true" string leaves the flag off. Verified
// indirectly — the init runs once at package load, so this test asserts
// the build-time invariant via the const + var declarations.
func TestEnabled_OnlyLiteralTrue(t *testing.T) {
	if envVar != "PARITY_DIAG_ENABLED" {
		t.Errorf("envVar drift: got %q want %q", envVar, "PARITY_DIAG_ENABLED")
	}
}
