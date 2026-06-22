package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestLegacyGraphdriverImageIdentityResolveTag(t *testing.T) {
	imageRoot, imageID := setupLegacyImageIdentityRoot(t)

	resolver, err := newLegacyGraphdriverImageIdentityBackend("overlay2", imageRoot)
	assert.NilError(t, err)

	resolved, err := resolver.ResolveImageID(context.Background(), "alpine")
	assert.NilError(t, err)
	assert.Check(t, is.Equal(resolved.String(), imageID.String()))
}

func TestLegacyGraphdriverImageIdentityResolveShortID(t *testing.T) {
	imageRoot, imageID := setupLegacyImageIdentityRoot(t)

	resolver, err := newLegacyGraphdriverImageIdentityBackend("overlay2", imageRoot)
	assert.NilError(t, err)

	resolved, err := resolver.ResolveImageID(context.Background(), imageID.Encoded()[:12])
	assert.NilError(t, err)
	assert.Check(t, is.Equal(resolved.String(), imageID.String()))
}

func TestLegacyGraphdriverImageIdentityMissingRef(t *testing.T) {
	imageRoot, _ := setupLegacyImageIdentityRoot(t)

	resolver, err := newLegacyGraphdriverImageIdentityBackend("overlay2", imageRoot)
	assert.NilError(t, err)

	_, err = resolver.ResolveImageID(context.Background(), "busybox")
	assert.Check(t, is.ErrorContains(err, "No such image: busybox:latest"))
}

func setupLegacyImageIdentityRoot(t *testing.T) (string, digest.Digest) {
	t.Helper()

	imageRoot := t.TempDir()
	imageID := digest.FromBytes([]byte("legacy image config"))
	contentDir := filepath.Join(imageRoot, "imagedb", "content", string(digest.Canonical))
	assert.NilError(t, os.MkdirAll(contentDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(contentDir, imageID.Encoded()), []byte("{}"), 0o600))

	repositories := struct {
		Repositories map[string]map[string]digest.Digest
	}{
		Repositories: map[string]map[string]digest.Digest{
			"alpine": {
				"alpine:latest": imageID,
			},
		},
	}
	repositoriesJSON, err := json.Marshal(repositories)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(imageRoot, "repositories.json"), repositoriesJSON, 0o600))

	return imageRoot, imageID
}
