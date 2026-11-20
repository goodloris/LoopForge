package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The product is Bagel. "LoopForge" survives only in internal identifiers
// (LoopForgeGitSSH globals, loopforge.* storage keys, LOOPFORGE_* env, the
// ghcr image and .loopforge/verify) and in links to the upstream repository
// (github.com/.../LoopForge), never as a word a user can read in the console
// or chat UI. The leading [^/…] drops repo-path URLs; the trailing [^…] drops
// longer identifiers such as LoopForgeGitSSHHost; case keeps lowercase
// loopforge and all-caps LOOPFORGE out.
var visibleOldBrand = regexp.MustCompile(`(^|[^/A-Za-z0-9_])LoopForge([^A-Za-z0-9_]|$)`)

func TestUserFacingFrontendSaysBagel(t *testing.T) {
	roots := []string{"app", "extensions", "../chatbot/src", "../chatbot/public"}
	files := []string{"../chatbot/index.html", "mcp-setup.txt"}
	for _, pattern := range []string{"*.js", "*.html"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			switch filepath.Ext(path) {
			case ".js", ".jsx", ".html", ".json", ".txt":
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.js") || strings.HasSuffix(file, ".test.js") || strings.HasSuffix(file, ".test.jsx") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for number, line := range strings.Split(string(data), "\n") {
			if visibleOldBrand.MatchString(line) {
				t.Errorf("%s:%d still shows the old product name: %s", file, number+1, strings.TrimSpace(line))
			}
		}
	}
}

// The chat sidebar rail brands with the Bagel ring, not the old workflow glyph.
func TestChatRailUsesBagelMark(t *testing.T) {
	app, err := os.ReadFile("../chatbot/src/App.jsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "<BagelMark") {
		t.Error("chatbot App rail should render <BagelMark /> as the brand logo")
	}
	mark, err := os.ReadFile("../chatbot/src/BagelMark.jsx")
	if err != nil {
		t.Fatal(err)
	}
	// The crust arc shared with web/bagel-mark.svg: the ring, not a placeholder.
	if !strings.Contains(string(mark), "A18 18 0 1 1") {
		t.Error("BagelMark should draw the Bagel ring (crust arc from bagel-mark.svg)")
	}
}
