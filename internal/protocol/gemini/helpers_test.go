package gemini

import (
	"os"
	"testing"
)

// readFile is a tiny helper shared by the test functions in this package
// (kept separate so fixtures stay loadable from any test).
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}
