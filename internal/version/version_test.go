package version

import "testing"

func TestBuildContainsFields(t *testing.T) {
	t.Parallel()
	version, commit, date := Build()
	if version == "" || commit == "" || date == "" {
		t.Fatalf("Build() = %q, %q, %q", version, commit, date)
	}
	if Current() != version {
		t.Fatalf("Current() = %q, want %q", Current(), version)
	}
}
