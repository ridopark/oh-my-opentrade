package realism

import "fmt"

// sprintf wraps fmt.Sprintf so the package-level fmtf helper can keep the
// flag-building code terse without leaking fmt into the test goldens.
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }
