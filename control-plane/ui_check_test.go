package controlplane

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// inlineModuleScript matches a page's single inline ESM script; both pages
// carry exactly one.
var inlineModuleScript = regexp.MustCompile(`(?s)<script type="module">(.*?)</script>`)

// TestUIModuleGraph validates the embedded frontend's module graph — syntax,
// specifier resolution, and that every named import exists on its target — by
// bundling both pages' inline scripts with esbuild's resolver, the same
// module resolution a browser performs. esbuild is a hard test prerequisite so
// local `go test` is exactly as strict as CI.
func TestUIModuleGraph(t *testing.T) {
	esbuild, err := exec.LookPath("esbuild")
	if err != nil {
		t.Fatal("esbuild not found on PATH (install it, e.g. `npm install -g esbuild`); the UI module check must not be skipped")
	}

	const ui = "ui"
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mirror the JS tree so the relative specifiers written below resolve.
	jsDir, err := os.ReadDir(filepath.Join(ui, "js"))
	if err != nil {
		t.Fatalf("read ui/js: %v", err)
	}
	for _, e := range jsDir {
		if e.IsDir() {
			t.Fatalf("unexpected subdirectory ui/js/%s", e.Name())
		}
		if err := copyFile(filepath.Join(ui, "js", e.Name()), filepath.Join(root, "js", e.Name())); err != nil {
			t.Fatalf("copy ui/js/%s: %v", e.Name(), err)
		}
	}

	var entries []string
	for _, name := range []string{"index.html", "admin.html"} {
		page, err := os.ReadFile(filepath.Join(ui, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		m := inlineModuleScript.FindSubmatch(page)
		if m == nil {
			t.Fatalf("%s has no inline module script", name)
		}
		// Rewrite root-absolute specifiers (/js/x) to filesystem-relative ones
		// (with a leading ./; esbuild treats a bare "js/..." as a package path)
		// so esbuild can resolve them from the mirrored tree.
		script := strings.ReplaceAll(string(m[1]), `"/js/`, `"./js/`)
		entry := filepath.Join(root, name+".js")
		if err := os.WriteFile(entry, []byte(script), 0o644); err != nil {
			t.Fatalf("write %s: %v", entry, err)
		}
		entries = append(entries, filepath.Base(entry))
	}

	cmd := exec.Command(esbuild, append(entries, "--bundle", "--outdir="+filepath.Join(root, "out"))...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("UI module graph check failed:\n%s", out)
	}
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
