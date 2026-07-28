package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginInventoryIsCoveredByAutomationAndRoadmaps(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(filepath.Join("..", ".."))
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	ci := readInventoryFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	release := readInventoryFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), "-base") {
			continue
		}
		count++
		plugin := entry.Name()
		t.Run(plugin, func(t *testing.T) {
			if !strings.Contains(ci, plugin) {
				t.Errorf("%s is missing from the CI static-build matrix", plugin)
			}
			if !strings.Contains(release, plugin) {
				t.Errorf("%s is missing from the release allowlist", plugin)
			}
			roadmap := filepath.Join(root, "docs", "roadmap", plugin+".md")
			if info, err := os.Stat(roadmap); err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s is missing roadmap %s", plugin, filepath.ToSlash(roadmap))
			}
		})
	}
	if count == 0 {
		t.Fatal("no plugin composition roots found")
	}
}

func readInventoryFile(t *testing.T, path string) string {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
