package platemd_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/platemd"
)

func TestImporterMarkdownSubsetRoundTrips(t *testing.T) {
	markdown := "# Heading\n\nA **bold** [link](https://example.com).\n\n- one\n- two\n\n```go\nfmt.Println(\"ok\")\n```"
	plate, err := platemd.ToJSON(markdown, nil)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(plate, &nodes); err != nil {
		t.Fatalf("invalid Plate JSON: %v", err)
	}
	if len(nodes) < 4 {
		t.Fatalf("nodes=%s", plate)
	}
	roundTrip, err := platemd.FromJSON(plate, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Heading", "**bold**", "https://example.com", "fmt.Println"} {
		if !strings.Contains(roundTrip, want) {
			t.Fatalf("missing %q in:\n%s", want, roundTrip)
		}
	}
}

func TestEmptyMarkdownProducesValidJSON(t *testing.T) {
	plate, err := platemd.ToJSON("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(plate) != "[]" {
		t.Fatalf("plate=%s", plate)
	}
}
