package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The product is Loopool. "LoopForge" survives only in internal identifiers
// (LoopForgeGitSSH globals, loopforge.* storage keys, LOOPFORGE_* env, the
// ghcr image and .loopforge/verify) and in links to the upstream repository
// (github.com/.../LoopForge), never as a word a user can read in the console
// or chat UI. The leading [^/…] drops repo-path URLs; the trailing [^…] drops
// longer identifiers such as LoopForgeGitSSHHost; case keeps lowercase
// loopforge and all-caps LOOPFORGE out. "Bagel" and the earlier capitalisation
// "LooPool" have no internal identifiers left (the mark component is now
// LoopoolMark, and the trailing [^…] keeps that identifier out), so any
// readable Bagel or LooPool is a miss.
var visibleOldBrand = regexp.MustCompile(`(^|[^/A-Za-z0-9_])(LoopForge|Bagel|LooPool)([^A-Za-z0-9_]|$)`)

func TestUserFacingFrontendSaysLoopool(t *testing.T) {
	roots := []string{"app", "extensions", "../chatbot/src", "../chatbot/public"}
	files := []string{"../chatbot/index.html", "mcp-setup.txt"}
	for _, pattern := range []string{"*.js", "*.html", "*.svg"} {
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

// The chat sidebar rail brands with the Loopool ring, not the old workflow glyph.
func TestChatRailUsesLoopoolMark(t *testing.T) {
	app, err := os.ReadFile("../chatbot/src/App.jsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "<LoopoolMark") {
		t.Error("chatbot App rail should render <LoopoolMark /> as the brand logo")
	}
	mark, err := os.ReadFile("../chatbot/src/LoopoolMark.jsx")
	if err != nil {
		t.Fatal(err)
	}
	// The crust arc shared with web/loopool-mark.svg: the ring, not a placeholder.
	if !strings.Contains(string(mark), "A18 18 0 1 1") {
		t.Error("LoopoolMark should draw the Loopool ring (crust arc from loopool-mark.svg)")
	}
}

// The seed's user-visible labels say Loopool too: the welcome project name and
// the built-in SOP author tag (Workflow.owner, shown on the SOP card).
func TestSeedLabelsSayLoopool(t *testing.T) {
	for _, path := range []string{"../migrations/seed/001_public_catalog.sql", "../migrations/postgres/seed/001_public_catalog.sql", "../migrations/seed/002_welcome_project.sql", "../migrations/postgres/seed/002_welcome_project.sql"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, old := range []string{`"owner": "LoopForge"`, "LoopForge 体验项目", `"owner": "Bagel"`, "Bagel 体验项目", `"owner": "LooPool"`, "LooPool 体验项目"} {
			if strings.Contains(string(data), old) {
				t.Errorf("%s still seeds %s", path, old)
			}
		}
	}
}

// The old LoopForge logo was a "b" curve stroked with a #3b8eea → #8b5cf6
// gradient, and the home title sweep lit up in its cyan/blue/violet palette.
// The boot loader and the chat home show the Loopool ring and its crust
// colours instead.
func TestBootLoaderAndHomeDropOldLogo(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	loader := regexp.MustCompile(`(?s)<svg class="boot-loader-logo".*?</svg>`).FindString(string(html))
	if !strings.Contains(loader, "A18 18 0 1 1") {
		t.Error("the boot loader should draw the Loopool ring (crust arc from loopool-mark.svg)")
	}
	oldLogo := regexp.MustCompile(`(?i)#3b8eea|#8b5cf6|#526ce8|M10\.5 27 A11\.5|--home-logo-(cyan|blue|violet)`)
	for _, path := range []string{"index.html", "login.html", "../chatbot/index.html", "../chatbot/src/HomeWelcome.jsx", "../chatbot/src/HomeLogo.jsx", "../chatbot/src/home-welcome.css", "../chatbot/src/home-logo.css"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if found := oldLogo.FindString(string(data)); found != "" {
			t.Errorf("%s still carries the old LoopForge logo (%s)", path, found)
		}
	}
}
