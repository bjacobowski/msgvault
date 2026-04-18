// Tool: htmlanalysis
//
// Analyzes HTML body structures across the email archive to identify distinct
// archetypes for building a test suite for HTML body parsing (StripHTML, etc.).
//
// Reads raw MIME from message_raw, parses HTML bodies, extracts structural
// fingerprints, clusters them, and outputs representative examples.
//
// Usage:
//
//	go run ./tools/htmlanalysis [--db path] [--limit N] [--output dir]
package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jhillyerd/enmime"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/html"
)

// Structural signals extracted from each HTML body.
type HTMLSignals struct {
	MessageID int    `json:"message_id"`
	SizeBytes int    `json:"size_bytes"`
	SizeClass string `json:"size_class"` // tiny/small/medium/large/huge

	// Tag analysis
	UniqueTagSet    string `json:"unique_tag_set"`    // sorted, comma-joined
	TagCount        int    `json:"tag_count"`         // total tag count
	UniqueTagCount  int    `json:"unique_tag_count"`  // distinct tags
	MaxNestingDepth int    `json:"max_nesting_depth"` // deepest DOM level
	HasFullDocument bool   `json:"has_full_document"` // has <html> or <body>
	HasDoctype      bool   `json:"has_doctype"`       // has <!DOCTYPE>
	IsFragment      bool   `json:"is_fragment"`       // no <html>/<body>, just content

	// Content-stripping tags
	HasScript bool `json:"has_script"`
	HasStyle  bool `json:"has_style"`
	HasHead   bool `json:"has_head"`

	// Layout classification
	TableNestingDepth int  `json:"table_nesting_depth"` // max nested <table> depth
	IsTableLayout     bool `json:"is_table_layout"`     // table depth >= 2
	HasList           bool `json:"has_list"`            // <ul>, <ol>, <dl>
	HasPreformatted   bool `json:"has_pre"`             // <pre> or <code>

	// Email-specific patterns
	HasQuotedContent bool   `json:"has_quoted_content"` // blockquote, gmail_quote, etc.
	QuoteType        string `json:"quote_type"`         // gmail_quote, moz-cite, outlook, blockquote, none
	HasSignature     bool   `json:"has_signature"`      // common signature patterns

	// Entity/encoding
	HasNamedEntities   bool `json:"has_named_entities"`   // &nbsp; &amp; etc.
	HasNumericEntities bool `json:"has_numeric_entities"` // &#160; &#x00A0; etc.
	HasNonASCII        bool `json:"has_non_ascii"`
	HasNBSP            bool `json:"has_nbsp"` // &nbsp; or &#160;

	// CSS complexity
	InlineStyleCount int  `json:"inline_style_count"` // elements with style=
	HasStyleBlock    bool `json:"has_style_block"`    // <style> block
	HasClassAttrs    bool `json:"has_class_attrs"`

	// Link/image density
	LinkCount  int `json:"link_count"`
	ImageCount int `json:"image_count"`

	// Whitespace patterns
	BRCount       int  `json:"br_count"`
	NBSPRunsCount int  `json:"nbsp_runs_count"` // sequences of 2+ &nbsp;
	HasBRRuns     bool `json:"has_br_runs"`     // 3+ consecutive <br>

	// Structural skeleton (tags-only, abstracted)
	SkeletonHash string `json:"skeleton_hash"` // hash of structural skeleton
	BlockSeq     string `json:"block_seq"`     // sequence of top-level block tags

	// Composite fingerprint for clustering
	ArchetypeKey string `json:"archetype_key"`
}

func main() {
	dbPath := flag.String("db", filepath.Join(os.Getenv("HOME"), ".msgvault", "msgvault.db"), "path to msgvault.db")
	limit := flag.Int("limit", 0, "limit number of messages to analyze (0 = all)")
	outputDir := flag.String("output", "html_analysis_output", "output directory")
	flag.Parse()

	// Windows HOME fallback
	if *dbPath == filepath.Join("", ".msgvault", "msgvault.db") {
		home, _ := os.UserHomeDir()
		*dbPath = filepath.Join(home, ".msgvault", "msgvault.db")
	}

	db, err := sql.Open("sqlite3", *dbPath+"?mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Query raw MIME data
	query := `SELECT mr.message_id, mr.raw_data, mr.compression 
		FROM message_raw mr
		JOIN message_bodies mb ON mb.message_id = mr.message_id
		WHERE mb.body_html IS NOT NULL AND mb.body_html != ''`
	if *limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", *limit)
	}

	rows, err := db.Query(query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Query failed: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	var allSignals []HTMLSignals
	archetypeGroups := make(map[string][]HTMLSignals)
	processed := 0
	errors := 0

	for rows.Next() {
		var msgID int
		var rawData []byte
		var compression sql.NullString

		if err := rows.Scan(&msgID, &rawData, &compression); err != nil {
			errors++
			continue
		}

		// Decompress
		if compression.Valid && compression.String == "zlib" {
			r, err := zlib.NewReader(bytes.NewReader(rawData))
			if err != nil {
				errors++
				continue
			}
			rawData, err = io.ReadAll(r)
			r.Close()
			if err != nil {
				errors++
				continue
			}
		}

		// Parse MIME
		env, err := enmime.ReadEnvelope(bytes.NewReader(rawData))
		if err != nil {
			errors++
			continue
		}

		htmlBody := env.HTML
		if htmlBody == "" {
			continue
		}

		signals := analyzeHTML(msgID, htmlBody)
		allSignals = append(allSignals, signals)
		archetypeGroups[signals.ArchetypeKey] = append(archetypeGroups[signals.ArchetypeKey], signals)

		processed++
		if processed%1000 == 0 {
			fmt.Fprintf(os.Stderr, "Processed %d messages (%d archetypes so far)...\n",
				processed, len(archetypeGroups))
		}
	}

	fmt.Fprintf(os.Stderr, "\n=== Analysis Complete ===\n")
	fmt.Fprintf(os.Stderr, "Processed: %d messages\n", processed)
	fmt.Fprintf(os.Stderr, "Errors: %d\n", errors)
	fmt.Fprintf(os.Stderr, "Distinct archetypes: %d\n", len(archetypeGroups))

	// Create output
	os.MkdirAll(*outputDir, 0o755)

	// Write full signals JSONL
	writeJSONL(filepath.Join(*outputDir, "all_signals.jsonl"), allSignals)

	// Write archetype summary
	writeArchetypeSummary(filepath.Join(*outputDir, "archetypes.json"), archetypeGroups)

	// Write dimensional analysis (what dimensions matter)
	writeDimensionalAnalysis(filepath.Join(*outputDir, "dimensions.txt"), allSignals)

	// Write recommended test cases
	writeTestCaseRecommendations(filepath.Join(*outputDir, "test_cases.json"), archetypeGroups, db)

	fmt.Fprintf(os.Stderr, "\nOutput written to %s/\n", *outputDir)
}

func analyzeHTML(msgID int, htmlBody string) HTMLSignals {
	s := HTMLSignals{MessageID: msgID, SizeBytes: len(htmlBody)}

	// Size class
	switch {
	case s.SizeBytes < 100:
		s.SizeClass = "tiny"
	case s.SizeBytes < 1000:
		s.SizeClass = "small"
	case s.SizeBytes < 10000:
		s.SizeClass = "medium"
	case s.SizeBytes < 100000:
		s.SizeClass = "large"
	default:
		s.SizeClass = "huge"
	}

	// Parse HTML DOM
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err == nil {
		walkDOM(doc, &s, 0, 0)
	}

	// Tag set analysis
	tags := extractTags(htmlBody)
	tagSet := uniqueSorted(tags)
	s.UniqueTagSet = strings.Join(tagSet, ",")
	s.TagCount = len(tags)
	s.UniqueTagCount = len(tagSet)

	// Document structure
	lowerHTML := strings.ToLower(htmlBody)
	s.HasFullDocument = strings.Contains(lowerHTML, "<html") || strings.Contains(lowerHTML, "<body")
	s.HasDoctype = strings.Contains(lowerHTML, "<!doctype")
	s.IsFragment = !s.HasFullDocument

	// Content-stripping tags
	s.HasScript = strings.Contains(lowerHTML, "<script")
	s.HasStyle = strings.Contains(lowerHTML, "<style")
	s.HasHead = strings.Contains(lowerHTML, "<head")

	// Layout
	s.HasList = strings.Contains(lowerHTML, "<ul") || strings.Contains(lowerHTML, "<ol") || strings.Contains(lowerHTML, "<dl")
	s.HasPreformatted = strings.Contains(lowerHTML, "<pre") || strings.Contains(lowerHTML, "<code")
	s.IsTableLayout = s.TableNestingDepth >= 2

	// Email-specific quoting
	s.HasQuotedContent, s.QuoteType = detectQuoting(htmlBody)
	s.HasSignature = detectSignature(htmlBody)

	// Entities
	s.HasNamedEntities = regexp.MustCompile(`&[a-zA-Z]+;`).MatchString(htmlBody)
	s.HasNumericEntities = regexp.MustCompile(`&#x?[0-9a-fA-F]+;`).MatchString(htmlBody)
	s.HasNonASCII = hasNonASCII(htmlBody)
	s.HasNBSP = strings.Contains(htmlBody, "&nbsp;") || strings.Contains(htmlBody, "&#160;") || strings.Contains(htmlBody, "&#xA0;")

	// CSS
	s.InlineStyleCount = strings.Count(lowerHTML, "style=\"") + strings.Count(lowerHTML, "style='")
	s.HasStyleBlock = strings.Contains(lowerHTML, "<style")
	s.HasClassAttrs = strings.Contains(lowerHTML, "class=\"") || strings.Contains(lowerHTML, "class='")

	// Links/images
	s.LinkCount = strings.Count(lowerHTML, "<a ")
	s.ImageCount = strings.Count(lowerHTML, "<img")

	// Whitespace patterns
	s.BRCount = strings.Count(lowerHTML, "<br")
	s.NBSPRunsCount = len(regexp.MustCompile(`(&nbsp;){2,}`).FindAllString(htmlBody, -1))
	s.HasBRRuns = regexp.MustCompile(`(<br\s*/?\s*>[\s]*){3,}`).MatchString(lowerHTML)

	// Structural skeleton
	s.SkeletonHash = computeSkeletonHash(htmlBody)
	s.BlockSeq = computeBlockSequence(htmlBody)

	// Composite archetype key
	s.ArchetypeKey = computeArchetypeKey(s)

	return s
}

func walkDOM(n *html.Node, s *HTMLSignals, depth int, tableDepth int) {
	if n.Type == html.ElementNode {
		if depth > s.MaxNestingDepth {
			s.MaxNestingDepth = depth
		}
		tag := strings.ToLower(n.Data)
		if tag == "table" {
			tableDepth++
			if tableDepth > s.TableNestingDepth {
				s.TableNestingDepth = tableDepth
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkDOM(c, s, depth+1, tableDepth)
	}
}

var tagRe = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9]*)[\s>]`)

func extractTags(htmlBody string) []string {
	matches := tagRe.FindAllStringSubmatch(htmlBody, -1)
	tags := make([]string, 0, len(matches))
	for _, m := range matches {
		tags = append(tags, strings.ToLower(m[1]))
	}
	return tags
}

func uniqueSorted(items []string) []string {
	seen := make(map[string]bool)
	for _, item := range items {
		seen[item] = true
	}
	result := make([]string, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

var (
	gmailQuoteRe   = regexp.MustCompile(`(?i)class=["']?gmail_quote`)
	mozCiteRe      = regexp.MustCompile(`(?i)class=["']?moz-cite`)
	outlookQuoteRe = regexp.MustCompile(`(?i)(class=["']?OutlookMessageHeader|<!--\s*\[if\s+gte\s+mso)`)
)

func detectQuoting(htmlBody string) (bool, string) {
	if gmailQuoteRe.MatchString(htmlBody) {
		return true, "gmail_quote"
	}
	if mozCiteRe.MatchString(htmlBody) {
		return true, "moz-cite"
	}
	if outlookQuoteRe.MatchString(htmlBody) {
		return true, "outlook"
	}
	if strings.Contains(strings.ToLower(htmlBody), "<blockquote") {
		return true, "blockquote"
	}
	return false, "none"
}

var sigRe = regexp.MustCompile(`(?i)(class=["']?signature|id=["']?signature|class=["']?gmail_signature|-- <br)`)

func detectSignature(htmlBody string) bool {
	return sigRe.MatchString(htmlBody)
}

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

// computeSkeletonHash creates a structural fingerprint by extracting only tags
// (no text, no attributes), collapsing repeated siblings, and hashing.
func computeSkeletonHash(htmlBody string) string {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return "parse_error"
	}
	var skeleton strings.Builder
	buildSkeleton(doc, &skeleton)
	h := sha256.Sum256([]byte(skeleton.String()))
	return hex.EncodeToString(h[:8]) // 16-char hex, enough for grouping
}

func buildSkeleton(n *html.Node, sb *strings.Builder) {
	if n.Type == html.ElementNode {
		sb.WriteString("<")
		sb.WriteString(n.Data)
		sb.WriteString(">")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		buildSkeleton(c, sb)
	}
	if n.Type == html.ElementNode {
		sb.WriteString("</")
		sb.WriteString(n.Data)
		sb.WriteString(">")
	}
}

var blockTagNames = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "li": true, "tr": true,
	"blockquote": true, "pre": true, "table": true, "ul": true,
	"ol": true, "hr": true, "br": true, "section": true, "article": true,
}

func computeBlockSequence(htmlBody string) string {
	tags := extractTags(htmlBody)
	var blocks []string
	for _, t := range tags {
		if blockTagNames[t] {
			blocks = append(blocks, t)
		}
	}
	// Collapse runs: p,p,p,div,div → p*3,div*2
	if len(blocks) == 0 {
		return "none"
	}
	var collapsed []string
	current := blocks[0]
	count := 1
	for i := 1; i < len(blocks); i++ {
		if blocks[i] == current {
			count++
		} else {
			if count > 1 {
				collapsed = append(collapsed, fmt.Sprintf("%s*%d", current, count))
			} else {
				collapsed = append(collapsed, current)
			}
			current = blocks[i]
			count = 1
		}
	}
	if count > 1 {
		collapsed = append(collapsed, fmt.Sprintf("%s*%d", current, count))
	} else {
		collapsed = append(collapsed, current)
	}

	seq := strings.Join(collapsed, ",")
	// Truncate very long sequences
	if len(seq) > 200 {
		seq = seq[:200] + "..."
	}
	return seq
}

// computeArchetypeKey creates a composite key for clustering.
// Groups emails by their structural character, not exact content.
func computeArchetypeKey(s HTMLSignals) string {
	parts := []string{
		s.SizeClass,
	}

	// Document type
	if s.IsFragment {
		parts = append(parts, "frag")
	} else if s.HasDoctype {
		parts = append(parts, "full-doctype")
	} else {
		parts = append(parts, "full")
	}

	// Layout
	if s.IsTableLayout {
		parts = append(parts, "table-layout")
	} else if s.TableNestingDepth > 0 {
		parts = append(parts, "table-simple")
	} else {
		parts = append(parts, "no-table")
	}

	// Strippable content
	if s.HasScript || s.HasStyle {
		parts = append(parts, "has-strip-tags")
	}

	// Quoting
	if s.HasQuotedContent {
		parts = append(parts, "quoted-"+s.QuoteType)
	}

	// Special content
	if s.HasPreformatted {
		parts = append(parts, "pre")
	}
	if s.HasList {
		parts = append(parts, "list")
	}
	if s.HasBRRuns {
		parts = append(parts, "br-runs")
	}
	if s.HasNBSP && s.NBSPRunsCount > 0 {
		parts = append(parts, "nbsp-runs")
	}

	// Entities
	if s.HasNamedEntities && s.HasNumericEntities {
		parts = append(parts, "mixed-entities")
	} else if s.HasNumericEntities {
		parts = append(parts, "numeric-entities")
	}

	// Non-ASCII
	if s.HasNonASCII {
		parts = append(parts, "non-ascii")
	}

	return strings.Join(parts, "|")
}

func writeJSONL(path string, signals []HTMLSignals) {
	f, _ := os.Create(path)
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, s := range signals {
		enc.Encode(s)
	}
	fmt.Fprintf(os.Stderr, "Wrote %d records to %s\n", len(signals), path)
}

type archetypeSummary struct {
	Key          string `json:"key"`
	Count        int    `json:"count"`
	ExampleMsgID int    `json:"example_message_id"`
	MinSize      int    `json:"min_size"`
	MaxSize      int    `json:"max_size"`
}

func writeArchetypeSummary(path string, groups map[string][]HTMLSignals) {
	summaries := make([]archetypeSummary, 0, len(groups))
	for key, members := range groups {
		minSize := members[0].SizeBytes
		maxSize := members[0].SizeBytes
		smallestIdx := 0
		for i, m := range members {
			if m.SizeBytes < minSize {
				minSize = m.SizeBytes
				smallestIdx = i
			}
			if m.SizeBytes > maxSize {
				maxSize = m.SizeBytes
			}
		}
		summaries = append(summaries, archetypeSummary{
			Key:          key,
			Count:        len(members),
			ExampleMsgID: members[smallestIdx].MessageID,
			MinSize:      minSize,
			MaxSize:      maxSize,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Count > summaries[j].Count
	})

	f, _ := os.Create(path)
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(summaries)
	fmt.Fprintf(os.Stderr, "Wrote %d archetypes to %s\n", len(summaries), path)
}

func writeDimensionalAnalysis(path string, signals []HTMLSignals) {
	f, _ := os.Create(path)
	defer f.Close()

	total := len(signals)
	fmt.Fprintf(f, "=== HTML Body Dimensional Analysis (%d messages) ===\n\n", total)

	// Size distribution
	sizeDist := make(map[string]int)
	for _, s := range signals {
		sizeDist[s.SizeClass]++
	}
	fmt.Fprintf(f, "--- Size Distribution ---\n")
	for _, cls := range []string{"tiny", "small", "medium", "large", "huge"} {
		fmt.Fprintf(f, "  %-8s %5d (%4.1f%%)\n", cls, sizeDist[cls], pct(sizeDist[cls], total))
	}

	// Boolean flags
	fmt.Fprintf(f, "\n--- Feature Prevalence ---\n")
	countBool := func(name string, pred func(HTMLSignals) bool) {
		count := 0
		for _, s := range signals {
			if pred(s) {
				count++
			}
		}
		fmt.Fprintf(f, "  %-25s %5d (%4.1f%%)\n", name, count, pct(count, total))
	}
	countBool("Full document", func(s HTMLSignals) bool { return s.HasFullDocument })
	countBool("Fragment (no html/body)", func(s HTMLSignals) bool { return s.IsFragment })
	countBool("Has DOCTYPE", func(s HTMLSignals) bool { return s.HasDoctype })
	countBool("Has <script>", func(s HTMLSignals) bool { return s.HasScript })
	countBool("Has <style>", func(s HTMLSignals) bool { return s.HasStyle })
	countBool("Has <head>", func(s HTMLSignals) bool { return s.HasHead })
	countBool("Table layout (depth≥2)", func(s HTMLSignals) bool { return s.IsTableLayout })
	countBool("Has tables", func(s HTMLSignals) bool { return s.TableNestingDepth > 0 })
	countBool("Has lists", func(s HTMLSignals) bool { return s.HasList })
	countBool("Has <pre>/<code>", func(s HTMLSignals) bool { return s.HasPreformatted })
	countBool("Has quoted content", func(s HTMLSignals) bool { return s.HasQuotedContent })
	countBool("Has signature", func(s HTMLSignals) bool { return s.HasSignature })
	countBool("Named entities", func(s HTMLSignals) bool { return s.HasNamedEntities })
	countBool("Numeric entities", func(s HTMLSignals) bool { return s.HasNumericEntities })
	countBool("Non-ASCII content", func(s HTMLSignals) bool { return s.HasNonASCII })
	countBool("NBSP usage", func(s HTMLSignals) bool { return s.HasNBSP })
	countBool("NBSP runs (2+)", func(s HTMLSignals) bool { return s.NBSPRunsCount > 0 })
	countBool("BR runs (3+)", func(s HTMLSignals) bool { return s.HasBRRuns })
	countBool("Has inline styles", func(s HTMLSignals) bool { return s.InlineStyleCount > 0 })
	countBool("Has class attrs", func(s HTMLSignals) bool { return s.HasClassAttrs })

	// Quote types
	fmt.Fprintf(f, "\n--- Quote Types ---\n")
	quoteDist := make(map[string]int)
	for _, s := range signals {
		quoteDist[s.QuoteType]++
	}
	for _, qt := range []string{"none", "gmail_quote", "blockquote", "moz-cite", "outlook"} {
		fmt.Fprintf(f, "  %-20s %5d (%4.1f%%)\n", qt, quoteDist[qt], pct(quoteDist[qt], total))
	}

	// Top block sequences
	fmt.Fprintf(f, "\n--- Top 30 Block Sequences ---\n")
	blockDist := make(map[string]int)
	for _, s := range signals {
		blockDist[s.BlockSeq]++
	}
	type kv struct {
		k string
		v int
	}
	var sorted []kv
	for k, v := range blockDist {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
	for i, item := range sorted {
		if i >= 30 {
			break
		}
		seq := item.k
		if len(seq) > 80 {
			seq = seq[:80] + "..."
		}
		fmt.Fprintf(f, "  %5d  %s\n", item.v, seq)
	}

	// Unique skeleton hashes
	skelDist := make(map[string]int)
	for _, s := range signals {
		skelDist[s.SkeletonHash]++
	}
	fmt.Fprintf(f, "\n--- Structural Skeletons ---\n")
	fmt.Fprintf(f, "  Unique skeletons: %d\n", len(skelDist))
	var skelSorted []kv
	for k, v := range skelDist {
		skelSorted = append(skelSorted, kv{k, v})
	}
	sort.Slice(skelSorted, func(i, j int) bool { return skelSorted[i].v > skelSorted[j].v })
	fmt.Fprintf(f, "  Top 20 most common:\n")
	for i, item := range skelSorted {
		if i >= 20 {
			break
		}
		fmt.Fprintf(f, "    %5d  %s\n", item.v, item.k)
	}

	fmt.Fprintf(os.Stderr, "Wrote dimensional analysis to %s\n", path)
}

type testCase struct {
	ArchetypeKey   string   `json:"archetype_key"`
	MessageID      int      `json:"message_id"`
	SizeBytes      int      `json:"size_bytes"`
	WhyInteresting []string `json:"why_interesting"`
}

func writeTestCaseRecommendations(path string, groups map[string][]HTMLSignals, db *sql.DB) {
	var cases []testCase

	for key, members := range groups {
		// Pick smallest representative
		sort.Slice(members, func(i, j int) bool {
			return members[i].SizeBytes < members[j].SizeBytes
		})
		rep := members[0]

		reasons := explainInteresting(rep)
		cases = append(cases, testCase{
			ArchetypeKey:   key,
			MessageID:      rep.MessageID,
			SizeBytes:      rep.SizeBytes,
			WhyInteresting: reasons,
		})
	}

	// Also add edge cases: extremes within each dimension
	addEdgeCases(&cases, groups)

	sort.Slice(cases, func(i, j int) bool {
		return cases[i].ArchetypeKey < cases[j].ArchetypeKey
	})

	f, _ := os.Create(path)
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(cases)
	fmt.Fprintf(os.Stderr, "Wrote %d test case recommendations to %s\n", len(cases), path)
}

func explainInteresting(s HTMLSignals) []string {
	var reasons []string
	if s.IsFragment {
		reasons = append(reasons, "HTML fragment (no <html>/<body>)")
	}
	if s.HasDoctype {
		reasons = append(reasons, "Full document with DOCTYPE")
	}
	if s.HasScript {
		reasons = append(reasons, "Contains <script> tags to strip")
	}
	if s.HasStyle {
		reasons = append(reasons, "Contains <style> blocks to strip")
	}
	if s.IsTableLayout {
		reasons = append(reasons, fmt.Sprintf("Table-based layout (depth %d)", s.TableNestingDepth))
	}
	if s.HasPreformatted {
		reasons = append(reasons, "Has <pre>/<code> (whitespace-sensitive)")
	}
	if s.HasQuotedContent {
		reasons = append(reasons, "Quoted content: "+s.QuoteType)
	}
	if s.HasSignature {
		reasons = append(reasons, "Email signature detected")
	}
	if s.HasBRRuns {
		reasons = append(reasons, "Multiple consecutive <br> tags")
	}
	if s.NBSPRunsCount > 0 {
		reasons = append(reasons, "NBSP spacing runs")
	}
	if s.HasNonASCII {
		reasons = append(reasons, "Non-ASCII content")
	}
	if s.HasNumericEntities {
		reasons = append(reasons, "Numeric HTML entities")
	}
	if s.HasList {
		reasons = append(reasons, "List elements")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "Basic "+s.SizeClass+" HTML")
	}
	return reasons
}

func addEdgeCases(cases *[]testCase, groups map[string][]HTMLSignals) {
	// Flatten all signals
	var all []HTMLSignals
	for _, members := range groups {
		all = append(all, members...)
	}
	if len(all) == 0 {
		return
	}

	// Find extremes
	sort.Slice(all, func(i, j int) bool { return all[i].MaxNestingDepth > all[j].MaxNestingDepth })
	*cases = append(*cases, testCase{
		ArchetypeKey:   "EDGE:deepest-nesting",
		MessageID:      all[0].MessageID,
		SizeBytes:      all[0].SizeBytes,
		WhyInteresting: []string{fmt.Sprintf("Deepest nesting: %d levels", all[0].MaxNestingDepth)},
	})

	sort.Slice(all, func(i, j int) bool { return all[i].TagCount > all[j].TagCount })
	*cases = append(*cases, testCase{
		ArchetypeKey:   "EDGE:most-tags",
		MessageID:      all[0].MessageID,
		SizeBytes:      all[0].SizeBytes,
		WhyInteresting: []string{fmt.Sprintf("Most tags: %d", all[0].TagCount)},
	})

	sort.Slice(all, func(i, j int) bool { return all[i].LinkCount > all[j].LinkCount })
	*cases = append(*cases, testCase{
		ArchetypeKey:   "EDGE:most-links",
		MessageID:      all[0].MessageID,
		SizeBytes:      all[0].SizeBytes,
		WhyInteresting: []string{fmt.Sprintf("Most links: %d", all[0].LinkCount)},
	})

	sort.Slice(all, func(i, j int) bool { return all[i].InlineStyleCount > all[j].InlineStyleCount })
	*cases = append(*cases, testCase{
		ArchetypeKey:   "EDGE:most-inline-styles",
		MessageID:      all[0].MessageID,
		SizeBytes:      all[0].SizeBytes,
		WhyInteresting: []string{fmt.Sprintf("Most inline styles: %d", all[0].InlineStyleCount)},
	})

	// Smallest HTML body
	sort.Slice(all, func(i, j int) bool { return all[i].SizeBytes < all[j].SizeBytes })
	*cases = append(*cases, testCase{
		ArchetypeKey:   "EDGE:smallest",
		MessageID:      all[0].MessageID,
		SizeBytes:      all[0].SizeBytes,
		WhyInteresting: []string{fmt.Sprintf("Smallest HTML body: %d bytes", all[0].SizeBytes)},
	})

	// Largest
	*cases = append(*cases, testCase{
		ArchetypeKey:   "EDGE:largest",
		MessageID:      all[len(all)-1].MessageID,
		SizeBytes:      all[len(all)-1].SizeBytes,
		WhyInteresting: []string{fmt.Sprintf("Largest HTML body: %d bytes", all[len(all)-1].SizeBytes)},
	})
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}
