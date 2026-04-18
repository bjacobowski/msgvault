package mime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type goldenFixture struct {
	Filename      string `json:"filename"`
	Dimension     string `json:"dimension"`
	Pattern       string `json:"pattern"`
	Category      string `json:"category"`
	Prevalence    int    `json:"prevalence"`
	OriginalSize  int    `json:"original_size"`
	SanitizedSize int    `json:"sanitized_size"`
}

type goldenManifest struct {
	Fixtures []goldenFixture `json:"fixtures"`
}

func TestStripHTML_Golden(t *testing.T) {
	goldenDir := filepath.Join("testdata", "golden")

	// Load manifest
	manifestData, err := os.ReadFile(filepath.Join(goldenDir, "manifest.json"))
	if err != nil {
		t.Fatalf("Failed to read manifest: %v", err)
	}

	var manifest goldenManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Failed to parse manifest: %v", err)
	}

	// Deduplicate fixtures by filename (some files cover multiple atoms)
	seen := make(map[string]bool)
	for _, f := range manifest.Fixtures {
		if seen[f.Filename] {
			continue
		}
		seen[f.Filename] = true

		testName := strings.TrimSuffix(f.Filename, ".html")
		t.Run(testName, func(t *testing.T) {
			htmlPath := filepath.Join(goldenDir, f.Filename)
			txtPath := strings.TrimSuffix(htmlPath, ".html") + ".txt"

			htmlBytes, err := os.ReadFile(htmlPath)
			if err != nil {
				t.Fatalf("Failed to read HTML fixture: %v", err)
			}

			expectedBytes, err := os.ReadFile(txtPath)
			if err != nil {
				t.Fatalf("Failed to read expected output: %v", err)
			}

			got := StripHTML(string(htmlBytes))
			expected := string(expectedBytes)

			if got != expected {
				t.Errorf("StripHTML output mismatch for %s\n--- GOT (len=%d) ---\n%s\n--- EXPECTED (len=%d) ---\n%s",
					f.Filename, len(got), truncate(got, 500), len(expected), truncate(expected, 500))
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
