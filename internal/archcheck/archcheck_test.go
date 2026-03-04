package archcheck

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root not found from %q (missing go.mod)", wd)
		}
		dir = parent
	}
}

func scanImports(t *testing.T, dir string) map[string][]string {
	t.Helper()

	out := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		imports := make([]string, 0, len(file.Imports))
		for _, imp := range file.Imports {
			if imp == nil {
				continue
			}
			p := strings.TrimSpace(strings.Trim(imp.Path.Value, `"`))
			if p != "" {
				imports = append(imports, p)
			}
		}
		out[path] = imports
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", dir, err)
	}
	return out
}

func TestAgentDoesNotImportInfraChannels(t *testing.T) {
	root := findRepoRoot(t)
	agentDir := filepath.Join(root, "pkg", "agent")

	importsByFile := scanImports(t, agentDir)
	banned := map[string]bool{
		"github.com/sipeed/picoclaw/pkg/channels": true,
		"github.com/sipeed/picoclaw/pkg/httpapi":  true,
		"github.com/sipeed/picoclaw/pkg/media":    true,
	}

	var violations []string
	for file, imports := range importsByFile {
		for _, imp := range imports {
			if banned[imp] {
				rel, _ := filepath.Rel(root, file)
				violations = append(violations, rel+": "+imp)
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("architecture violation: pkg/agent imports infra packages:\n%s", strings.Join(violations, "\n"))
	}
}

func TestInternalCoreDoesNotImportAppOrPkg(t *testing.T) {
	root := findRepoRoot(t)
	coreDir := filepath.Join(root, "internal", "core")

	importsByFile := scanImports(t, coreDir)

	var violations []string
	for file, imports := range importsByFile {
		for _, imp := range imports {
			// internal/core is our "core boundary": it may only depend on itself
			// (and stdlib/third-party, though we try to keep it minimal).
			if strings.HasPrefix(imp, "github.com/sipeed/picoclaw/") &&
				!strings.HasPrefix(imp, "github.com/sipeed/picoclaw/internal/core/") {
				rel, _ := filepath.Rel(root, file)
				violations = append(violations, rel+": "+imp)
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("architecture violation: internal/core imports non-core repo packages:\n%s", strings.Join(violations, "\n"))
	}
}
