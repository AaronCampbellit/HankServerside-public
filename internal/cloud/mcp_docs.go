package cloud

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mcpDocsIndex exposes a curated, read-only slice of the project's documentation
// (and a build-time source snapshot) to the MCP endpoint. A fixed allowlist of
// top-level files plus the current docs/, schemas/, and code-reference/ subtrees
// is filtered through containment, extension, and temporary-document exclusions
// so plans, .env files, secrets, and the live source tree cannot be read.
// code-reference/ is a sanitized snapshot shipped by Dockerfile.server, never
// the running process's source.
type mcpDocsIndex struct {
	root string
}

var mcpDocFileAllowlist = []string{"README.md", "AGENTS.md"}
var mcpDocDirAllowlist = []string{"docs", "schemas", "code-reference"}
var mcpDocExcludedPrefixes = []string{"docs/superpowers/"}
var mcpDocTextExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".json": true,
	".yaml": true, ".yml": true, ".sql": true, ".toml": true,
	// Source-code extensions for the code-reference/ snapshot.
	".go": true, ".mod": true, ".html": true, ".css": true, ".js": true,
}

const mcpMaxDocBytes = 400_000

func newMCPDocsIndex(root string) *mcpDocsIndex {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || strings.TrimSpace(root) == "" {
		abs = root
	}
	return &mcpDocsIndex{root: abs}
}

func (d *mcpDocsIndex) isAllowedRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, prefix := range mcpDocExcludedPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return false
		}
	}
	for _, f := range mcpDocFileAllowlist {
		if rel == f {
			return true
		}
	}
	top := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		top = rel[:i]
	}
	for _, dir := range mcpDocDirAllowlist {
		if top == dir {
			return mcpDocTextExtensions[strings.ToLower(filepath.Ext(rel))]
		}
	}
	return false
}

func (d *mcpDocsIndex) listPaths() []string {
	seen := map[string]bool{}
	var paths []string
	add := func(rel string) {
		if !seen[rel] {
			seen[rel] = true
			paths = append(paths, rel)
		}
	}
	for _, name := range mcpDocFileAllowlist {
		if info, err := os.Stat(filepath.Join(d.root, name)); err == nil && !info.IsDir() {
			add(name)
		}
	}
	for _, dir := range mcpDocDirAllowlist {
		base := filepath.Join(d.root, dir)
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if strings.HasPrefix(entry.Name(), ".") && path != base {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".") {
				return nil
			}
			rel, relErr := filepath.Rel(d.root, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if d.isAllowedRel(rel) {
				if _, err := d.resolve(rel); err == nil {
					add(rel)
				}
			}
			return nil
		})
	}
	sort.Strings(paths)
	return paths
}

func (d *mcpDocsIndex) resolve(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	rel = filepath.ToSlash(strings.TrimLeft(rel, "/"))
	if !d.isAllowedRel(rel) {
		return "", fmt.Errorf("path %q is not an exposed document; use list_docs to see allowed paths", rel)
	}
	full := filepath.Clean(filepath.Join(d.root, filepath.FromSlash(rel)))
	rootWithSep := d.root
	if !strings.HasSuffix(rootWithSep, string(os.PathSeparator)) {
		rootWithSep += string(os.PathSeparator)
	}
	if !strings.HasPrefix(full, rootWithSep) {
		return "", fmt.Errorf("path escapes the docs root")
	}
	current := d.root
	var info os.FileInfo
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(part))
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("document not found: %s", rel)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("document not found: %s", rel)
		}
	}
	if info == nil || info.IsDir() {
		return "", fmt.Errorf("document not found: %s", rel)
	}
	return full, nil
}

func (d *mcpDocsIndex) read(rel string) (string, error) {
	full, err := d.resolve(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("document not found: %s", rel)
	}
	truncated := false
	if len(data) > mcpMaxDocBytes {
		data = data[:mcpMaxDocBytes]
		truncated = true
	}
	header := "# " + rel + "\n\n"
	if truncated {
		header += "_[truncated to first " + fmt.Sprintf("%d", mcpMaxDocBytes) + " bytes]_\n\n"
	}
	return header + string(data), nil
}

func (d *mcpDocsIndex) search(query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(query)) {
		terms = append(terms, t)
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}

	type result struct {
		rel     string
		score   int
		matches []string
	}
	var results []result
	for _, rel := range d.listPaths() {
		full := filepath.Join(d.root, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if len(data) > mcpMaxDocBytes {
			data = data[:mcpMaxDocBytes]
		}
		score := 0
		var matches []string
		for i, line := range strings.Split(string(data), "\n") {
			low := strings.ToLower(line)
			hit := false
			for _, term := range terms {
				if c := strings.Count(low, term); c > 0 {
					score += c
					hit = true
				}
			}
			if hit && len(matches) < 5 {
				snippet := strings.TrimSpace(line)
				if len(snippet) > 240 {
					snippet = snippet[:240] + "..."
				}
				matches = append(matches, fmt.Sprintf("  L%d: %s", i+1, snippet))
			}
		}
		if score > 0 {
			results = append(results, result{rel: rel, score: score, matches: matches})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].rel < results[j].rel
	})
	if len(results) > limit {
		results = results[:limit]
	}
	if len(results) == 0 {
		return "No documents matched: " + query, nil
	}
	var b strings.Builder
	for _, r := range results {
		plural := "es"
		if r.score == 1 {
			plural = ""
		}
		fmt.Fprintf(&b, "## %s  (%d match%s)\n", r.rel, r.score, plural)
		for _, m := range r.matches {
			b.WriteString(m)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
