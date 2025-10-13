//go:build nightly

package nightly_test

import (
	"testing"
	"time"
)

// TestNightlyExample is a dummy test that runs only when the nightly build tag is specified.
// Run this test with: go test -tags nightly ./test/nightly/...
// Or using the Makefile: make test-nightly
func TestNightlyExample(t *testing.T) {
	t.Log("Running nightly test example")

	// Simulate a longer-running test that would be suitable for nightly runs
	time.Sleep(100 * time.Millisecond)

	// Example assertions
	result := 2 + 2
	if result != 4 {
		t.Errorf("Expected 4, got %d", result)
	}

	t.Log("Nightly test completed successfully")
}

// TestNightlyLongRunning is another example of a nightly test.
func TestNightlyLongRunning(t *testing.T) {
	t.Log("Running long nightly test")

	// Simulate a test that takes longer to run
	for i := 0; i < 5; i++ {
		time.Sleep(50 * time.Millisecond)
		t.Logf("Long running test iteration %d", i+1)
	}

	t.Log("Long nightly test completed")
}
