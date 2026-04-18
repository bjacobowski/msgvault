//go:build ignore
// +build ignore

// Generates expected .txt output for golden HTML test fixtures by running
// the actual StripHTML function on each .html file.
//
// Usage: go run tools/gen_golden_expected.go [dir]
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesm/msgvault/internal/mime"
)

func main() {
	dir := "internal/mime/testdata/golden"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading dir: %v\n", err)
		os.Exit(1)
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}

		htmlPath := filepath.Join(dir, e.Name())
		txtPath := strings.TrimSuffix(htmlPath, ".html") + ".txt"

		htmlBytes, err := os.ReadFile(htmlPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", htmlPath, err)
			continue
		}

		result := mime.StripHTML(string(htmlBytes))

		if err := os.WriteFile(txtPath, []byte(result), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", txtPath, err)
			continue
		}

		count++
		fmt.Printf("  %s -> %s (%d bytes)\n", e.Name(), filepath.Base(txtPath), len(result))
	}

	fmt.Printf("\nGenerated %d expected output files\n", count)
}
