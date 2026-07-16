package realrepo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/tests/fixtures/realrepo"
)

// TestFastify_PinnedCheckout verifies the cached clone resolves to the
// pinned SHA and looks like a fastify tree. Offline-safe once the
// cache has been populated; otherwise this will shallow-clone once.
func TestFastify_PinnedCheckout(t *testing.T) {
	root := realrepo.Fastify(t)
	got, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	if !strings.Contains(string(got), `"name": "fastify"`) {
		t.Fatalf("package.json does not look like fastify:\n%.200s", got)
	}
	for _, rel := range []string{"fastify.js", filepath.Join("lib", "route.js")} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
}
