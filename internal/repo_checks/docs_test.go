package repo_checks

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestActiveDocsDoNotReferenceRemovedLegacyPaths(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	activeDocs := []string{filepath.Join(root, "README.md")}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "superpowers" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" || filepath.Base(path) == "legacy-code-audit.md" {
			return nil
		}
		activeDocs = append(activeDocs, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}

	forbiddenText := []string{
		"/dashboard/home-users",
		"/dashboard/service-profiles",
		"/dashboard/sync-status",
		"/dashboard/storage",
		"/dashboard/assistant-settings",
		"/dashboard/accept-invitation",
		"/v1/oauth/openai/callback",
	}
	forbiddenPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\bHANK_SMB_HOST\b`),
		regexp.MustCompile(`\bHANK_SMB_SHARE\b`),
		regexp.MustCompile(`\bHANK_SMB_USERNAME\b`),
		regexp.MustCompile(`\bHANK_SMB_PASSWORD\b`),
		regexp.MustCompile(`\bHANK_SMB_DOMAIN\b`),
	}
	for _, path := range activeDocs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s read: %v", path, err)
		}
		body := string(data)
		for _, value := range forbiddenText {
			if strings.Contains(body, value) {
				t.Fatalf("%s references removed legacy value %q", path, value)
			}
		}
		for _, pattern := range forbiddenPatterns {
			if pattern.MatchString(body) {
				t.Fatalf("%s references removed legacy env key %q", path, pattern.String())
			}
		}
	}
}

func TestObsoleteDocumentationIsRemoved(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, rel := range []string{
		"SERVER_SYNC.md",
		"docs/app-integration",
		"docs/backend-production-repair-plan.md",
		"docs/roadmap.md",
		"docs/project-knowledge-index.md",
		"docs/security-hardening-todo.md",
		"docs/agent-change-guardrails.md",
		"docs/remote-desktop/native-control-acceptance.md",
		"docs/remote-desktop/native-viewing-acceptance.md",
		"docs/remote-desktop/privileged-permission-acceptance.md",
		"docs/remote-desktop/synthetic-acceptance.md",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("obsolete documentation still exists: %s", rel)
		}
	}
}

func TestRetiredNamingIsConfinedToMigrationBoundary(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	allowed := map[string]bool{
		"docs/naming-migration.md":                       true,
		"scripts/bootstrap-first-run.sh":                 true,
		"scripts/migrate-hank-naming.sh":                 true,
		"scripts/reconcile-assistant-index-migration.sh": true, // Explicit maintenance-only environment conversion.
		"scripts/tests/hank-naming-migration-test.sh":    true,
	}
	retired := []string{
		"Hank " + "Remote",
		"HANK_" + "REMOTE_",
		"hank-" + "remote-cloud",
		"hank-" + "remote-agent",
		"github.com/dropfile/" + "hank",
		"/srv/hank-" + "remote",
		"hank_" + "remote_",
		"hank" + "remote",
	}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "data", "dist", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if allowed[rel] || rel == "internal/repo_checks/docs_test.go" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		base := filepath.Base(path)
		if ext != ".go" && ext != ".ts" && ext != ".tsx" && ext != ".js" &&
			ext != ".sh" && ext != ".md" && ext != ".yml" && ext != ".yaml" &&
			ext != ".conf" && base != "Dockerfile.server" && base != "Makefile" && base != "go.mod" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range retired {
			if strings.Contains(string(data), value) {
				t.Errorf("%s contains retired naming %q outside the migration boundary", rel, value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
}

var markdownLinkPattern = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)

func TestActiveMarkdownLinksResolve(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, source := range activeMarkdownFiles(t, root) {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		for _, match := range markdownLinkPattern.FindAllStringSubmatch(string(data), -1) {
			target := strings.Trim(strings.TrimSpace(match[1]), "<>")
			if target == "" || strings.HasPrefix(target, "#") ||
				strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "mailto:") {
				continue
			}
			if index := strings.IndexAny(target, "#?"); index >= 0 {
				target = target[:index]
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(source), filepath.FromSlash(target)))
			if _, err := os.Stat(resolved); err != nil {
				rel, _ := filepath.Rel(root, source)
				t.Errorf("%s links missing target %q: %v", filepath.ToSlash(rel), target, err)
			}
		}
	}
}

func TestDocumentationCatalogCoversActiveDocs(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "README.md"))
	if err != nil {
		t.Fatalf("read docs/README.md: %v", err)
	}
	catalog := string(data)
	for _, source := range activeMarkdownFiles(t, root) {
		docsRoot := filepath.Join(root, "docs")
		rel, err := filepath.Rel(docsRoot, source)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.Clean(rel) == "README.md" {
			continue
		}
		rel = filepath.ToSlash(rel)
		if !strings.Contains(catalog, "("+rel+")") {
			t.Errorf("docs/README.md does not catalog %s", rel)
		}
	}
}

func activeMarkdownFiles(t *testing.T, root string) []string {
	t.Helper()
	paths := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "RELEASE.md"),
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "superpowers" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	return paths
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
