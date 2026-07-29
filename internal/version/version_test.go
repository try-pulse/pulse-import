package version

import (
	"strings"
	"testing"
)

func TestStringContainsFields(t *testing.T) {
	t.Parallel()
	got := String()
	for _, want := range []string{Version, Commit, Date} {
		if !strings.Contains(got, want) {
			t.Fatalf("String()=%q missing %q", got, want)
		}
	}
}
