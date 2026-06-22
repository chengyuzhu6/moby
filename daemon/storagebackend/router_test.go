package storagebackend

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestRouterRestoreLayerUsesContainerDriver(t *testing.T) {
	defaultLayer := fakeRWLayer{name: "default-layer"}
	defaultBackend := &fakeBackend{
		id:     "overlayfs",
		layers: map[string]RWLayer{"new": defaultLayer},
	}
	legacyLayer := fakeRWLayer{name: "legacy-layer"}
	legacyBackend := &fakeBackend{
		id:     "overlay2",
		layers: map[string]RWLayer{"old": legacyLayer},
	}

	router, err := NewRouter(defaultBackend)
	assert.NilError(t, err)
	assert.NilError(t, router.RegisterLegacy(legacyBackend))

	newCtr := &ContainerRef{ID: "new", Driver: "overlayfs"}
	assert.NilError(t, router.RestoreLayer(newCtr))
	assert.Check(t, is.Equal(newCtr.RWLayer, defaultLayer))

	oldCtr := &ContainerRef{ID: "old", Driver: "overlay2"}
	assert.NilError(t, router.RestoreLayer(oldCtr))
	assert.Check(t, is.Equal(oldCtr.RWLayer, legacyLayer))

	assert.Check(t, is.DeepEqual(defaultBackend.getLayerCalls, []string{"new"}))
	assert.Check(t, is.DeepEqual(legacyBackend.getLayerCalls, []string{"old"}))
}

func TestRouterRestoreLayerFallsBackToDefaultForEmptyDriver(t *testing.T) {
	layer := fakeRWLayer{name: "default-layer"}
	backend := &fakeBackend{
		id:     "overlayfs",
		layers: map[string]RWLayer{"ctr": layer},
	}

	router, err := NewRouter(backend)
	assert.NilError(t, err)

	ctr := &ContainerRef{ID: "ctr"}
	assert.NilError(t, router.RestoreLayer(ctr))
	assert.Check(t, is.Equal(ctr.RWLayer, layer))
	assert.Check(t, is.DeepEqual(backend.getLayerCalls, []string{"ctr"}))
}

func TestRouterRestoreLayerRejectsUnknownBackend(t *testing.T) {
	router, err := NewRouter(&fakeBackend{id: "overlayfs"})
	assert.NilError(t, err)

	err = router.RestoreLayer(&ContainerRef{ID: "old", Driver: "overlay2"})
	assert.ErrorContains(t, err, `storage backend "overlay2"`)
}

func TestRouterReleaseLayerUsesContainerDriver(t *testing.T) {
	defaultBackend := &fakeBackend{id: "overlayfs"}
	legacyBackend := &fakeBackend{id: "overlay2"}

	router, err := NewRouter(defaultBackend)
	assert.NilError(t, err)
	assert.NilError(t, router.RegisterLegacy(legacyBackend))

	oldCtr := &ContainerRef{
		ID:      "old",
		Driver:  "overlay2",
		RWLayer: fakeRWLayer{name: "legacy-layer"},
	}
	assert.NilError(t, router.ReleaseLayer(oldCtr))

	assert.Check(t, is.Equal(defaultBackend.releaseCalls, 0))
	assert.Check(t, is.Equal(legacyBackend.releaseCalls, 1))
}

func TestRouterGetLayerMountIDUsesContainerDriver(t *testing.T) {
	defaultBackend := &fakeBackend{id: "overlayfs"}
	legacyBackend := &fakeBackend{id: "overlay2"}

	router, err := NewRouter(defaultBackend)
	assert.NilError(t, err)
	assert.NilError(t, router.RegisterLegacy(legacyBackend))

	mountID, err := router.GetLayerMountID(&ContainerRef{
		ID:     "old",
		Driver: "overlay2",
	})
	assert.NilError(t, err)
	assert.Check(t, is.Equal(mountID, "old-mount"))
	assert.Check(t, is.DeepEqual(defaultBackend.getMountIDCalls, []string(nil)))
	assert.Check(t, is.DeepEqual(legacyBackend.getMountIDCalls, []string{"old"}))
}

func TestRouterResolveImageIDUsesContainerDriver(t *testing.T) {
	defaultBackend := &fakeBackend{
		id:       "overlayfs",
		imageIDs: map[string]string{"alpine": "sha256:new"},
	}
	legacyBackend := &fakeBackend{
		id:       "overlay2",
		imageIDs: map[string]string{"alpine": "sha256:old"},
	}

	router, err := NewRouter(defaultBackend)
	assert.NilError(t, err)
	assert.NilError(t, router.RegisterLegacy(legacyBackend))

	imageID, err := router.ResolveImageID(context.Background(), &ContainerRef{
		ID:     "old",
		Driver: "overlay2",
	}, "alpine")
	assert.NilError(t, err)
	assert.Check(t, is.Equal(imageID, "sha256:old"))
	assert.Check(t, is.DeepEqual(defaultBackend.resolveImageCalls, []string(nil)))
	assert.Check(t, is.DeepEqual(legacyBackend.resolveImageCalls, []string{"alpine"}))
}

func TestRouterCleanupCleansAllBackends(t *testing.T) {
	defaultBackend := &fakeBackend{id: "overlayfs"}
	legacyBackend := &fakeBackend{id: "overlay2"}

	router, err := NewRouter(defaultBackend)
	assert.NilError(t, err)
	assert.NilError(t, router.RegisterLegacy(legacyBackend))

	assert.NilError(t, router.Cleanup())
	assert.Check(t, is.Equal(defaultBackend.cleanupCalls, 1))
	assert.Check(t, is.Equal(legacyBackend.cleanupCalls, 1))
}

func TestRouterRejectsDuplicateBackend(t *testing.T) {
	router, err := NewRouter(&fakeBackend{id: "overlayfs"})
	assert.NilError(t, err)

	err = router.RegisterLegacy(&fakeBackend{id: "overlayfs"})
	assert.ErrorContains(t, err, "already registered")
}

type fakeBackend struct {
	id                BackendID
	layers            map[string]RWLayer
	imageIDs          map[string]string
	getLayerCalls     []string
	getMountIDCalls   []string
	resolveImageCalls []string
	releaseCalls      int
	cleanupCalls      int
}

func (b *fakeBackend) BackendID() BackendID {
	return b.id
}

func (b *fakeBackend) GetLayerByID(containerID string) (RWLayer, error) {
	b.getLayerCalls = append(b.getLayerCalls, containerID)
	layer, ok := b.layers[containerID]
	if !ok {
		return nil, errors.New("layer not found")
	}
	return layer, nil
}

func (b *fakeBackend) ReleaseLayer(RWLayer) error {
	b.releaseCalls++
	return nil
}

func (b *fakeBackend) GetLayerMountID(containerID string) (string, error) {
	b.getMountIDCalls = append(b.getMountIDCalls, containerID)
	return containerID + "-mount", nil
}

func (b *fakeBackend) ResolveImageID(_ context.Context, refOrID string) (string, error) {
	b.resolveImageCalls = append(b.resolveImageCalls, refOrID)
	imageID, ok := b.imageIDs[refOrID]
	if !ok {
		return "", errors.New("image not found")
	}
	return imageID, nil
}

func (b *fakeBackend) Cleanup() error {
	b.cleanupCalls++
	return nil
}

type fakeRWLayer struct {
	name string
}

func (l fakeRWLayer) Mount(string) (string, error) {
	return "/tmp/" + l.name, nil
}

func (l fakeRWLayer) Unmount() error {
	return nil
}

func (l fakeRWLayer) Metadata() (map[string]string, error) {
	return map[string]string{"name": l.name}, nil
}
