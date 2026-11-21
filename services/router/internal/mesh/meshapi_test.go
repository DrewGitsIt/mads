package mesh

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The Router and the Mesh Service bump MeshAPI together: a peer whose hello
// says another version reads version_mismatch and isn't used, so the two
// constants disagreeing would grey out every peer on the same build.
func TestMeshAPIMatchesTheMeshService(t *testing.T) {
	src, err := os.ReadFile("../../../mesh/internal/server/server.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const MeshAPI = (\d+)$`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no MeshAPI const in services/mesh/internal/server/server.go")
	}
	if n, _ := strconv.Atoi(string(m[1])); n != MeshAPI {
		t.Fatalf("Mesh Service MeshAPI %d, Router %d", n, MeshAPI)
	}
	if MeshAPI < 2 {
		t.Fatalf("MeshAPI %d: builds without GET /events/head over the mesh are 1", MeshAPI)
	}
}
