package cli

import (
	"testing"
	"time"
)

// PinTimeForTest fixes what "now" means for the external test package, so a
// test of the body or parameter path can assert an exact timestamp instead of
// a tolerance around the wall clock. This file is only compiled into tests.
func PinTimeForTest(t *testing.T, now time.Time) {
	t.Helper()

	original := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = original })
}
