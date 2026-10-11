package cli

import (
	"testing"
	"time"
)

// PinTimeForTest fixes "now" for the external test package, so a body or
// parameter test can assert an exact timestamp rather than a tolerance.
func PinTimeForTest(t *testing.T, now time.Time) {
	t.Helper()

	original := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = original })
}
