package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/containerd/log"
	"github.com/distribution/reference"
	"github.com/moby/moby/v2/daemon/container"
	"github.com/moby/moby/v2/daemon/images"
	"github.com/moby/moby/v2/daemon/internal/image"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/moby/moby/v2/daemon/storagebackend"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/go-digest/digestset"
)

type imageIdentityBackend interface {
	BackendID() storagebackend.BackendID
	ResolveImageID(ctx context.Context, refOrID string) (image.ID, error)
}

type containerImageIdentityResolver struct {
	defaultBackend imageIdentityBackend
	backends       map[storagebackend.BackendID]imageIdentityBackend
}

func newContainerImageIdentityResolver(defaultBackend imageIdentityBackend) *containerImageIdentityResolver {
	id := defaultBackend.BackendID()
	return &containerImageIdentityResolver{
		defaultBackend: defaultBackend,
		backends: map[storagebackend.BackendID]imageIdentityBackend{
			id: defaultBackend,
		},
	}
}

func (r *containerImageIdentityResolver) registerLegacy(backend imageIdentityBackend) {
	r.backends[backend.BackendID()] = backend
}

func (r *containerImageIdentityResolver) backendForContainer(ctr *container.Container) imageIdentityBackend {
	if ctr == nil || ctr.Driver == "" {
		return r.defaultBackend
	}
	backend, ok := r.backends[storagebackend.BackendID(ctr.Driver)]
	if !ok {
		return r.defaultBackend
	}
	return backend
}

func (r *containerImageIdentityResolver) Resolve(ctx context.Context, ctr *container.Container, refOrID string) (image.ID, error) {
	return r.backendForContainer(ctr).ResolveImageID(ctx, refOrID)
}

func (daemon *Daemon) initContainerImageIdentityResolver(ctx context.Context, cfg *configStore, containers map[string]map[string]*container.Container, currentDriver string) {
	resolver := newContainerImageIdentityResolver(imageServiceIdentityBackend{imageService: daemon.imageService})

	for driver, driverContainers := range containers {
		if driver == "" || driver == currentDriver || len(driverContainers) == 0 {
			continue
		}

		backend, err := newLegacyGraphdriverImageIdentityBackend(driver, filepath.Join(cfg.Root, "image", driver))
		if err != nil {
			log.G(ctx).WithFields(log.Fields{
				"driver":    driver,
				"container": len(driverContainers),
				"error":     err,
			}).Warn("legacy image identity is not available; containers using it may show image IDs")
			continue
		}
		resolver.registerLegacy(backend)
		log.G(ctx).WithFields(log.Fields{
			"driver":    driver,
			"container": len(driverContainers),
		}).Info("registered legacy image identity resolver for previously-created containers")
	}

	daemon.imageIdentity = resolver
}

func (daemon *Daemon) resolveContainerImageID(ctx context.Context, containerID, refOrID string) (image.ID, error) {
	if daemon.imageIdentity == nil || daemon.containers == nil {
		return imageServiceIdentityBackend{imageService: daemon.imageService}.ResolveImageID(ctx, refOrID)
	}

	ctr := daemon.containers.Get(containerID)
	imageID, err := daemon.imageIdentity.Resolve(ctx, ctr, refOrID)
	if err == nil {
		driver := ""
		if ctr != nil {
			driver = ctr.Driver
		}
		log.G(ctx).WithFields(log.Fields{
			"container": containerID,
			"driver":    driver,
			"image":     refOrID,
			"imageID":   imageID,
		}).Debug("resolved container image identity")
	}
	return imageID, err
}

type imageServiceIdentityBackend struct {
	imageService ImageService
}

func (b imageServiceIdentityBackend) BackendID() storagebackend.BackendID {
	return storagebackend.BackendID(b.imageService.StorageDriver())
}

func (b imageServiceIdentityBackend) ResolveImageID(ctx context.Context, refOrID string) (image.ID, error) {
	img, err := b.imageService.GetImage(ctx, refOrID, imagebackend.GetImageOpts{})
	if err != nil {
		return "", err
	}
	return img.ID(), nil
}

type legacyGraphdriverImageIdentityBackend struct {
	driverName     string
	imageContent   string
	repositories   map[string]map[string]digest.Digest
	imageDigestSet *digestset.Set
}

func newLegacyGraphdriverImageIdentityBackend(driverName, imageRoot string) (*legacyGraphdriverImageIdentityBackend, error) {
	backend := &legacyGraphdriverImageIdentityBackend{
		driverName:     driverName,
		imageContent:   filepath.Join(imageRoot, "imagedb", "content", string(digest.Canonical)),
		imageDigestSet: digestset.NewSet(),
	}
	if err := backend.loadRepositories(filepath.Join(imageRoot, "repositories.json")); err != nil {
		return nil, err
	}
	if err := backend.loadImageDigests(); err != nil {
		return nil, err
	}
	return backend, nil
}

func (b *legacyGraphdriverImageIdentityBackend) BackendID() storagebackend.BackendID {
	return storagebackend.BackendID(b.driverName)
}

func (b *legacyGraphdriverImageIdentityBackend) ResolveImageID(_ context.Context, refOrID string) (image.ID, error) {
	ref, err := reference.ParseAnyReference(refOrID)
	if err != nil {
		return "", err
	}

	namedRef, ok := ref.(reference.Named)
	if !ok {
		digested, ok := ref.(reference.Digested)
		if !ok {
			return "", images.ErrImageDoesNotExist{Ref: ref}
		}
		return b.resolveDigest(ref, digested.Digest())
	}

	if dgst, ok := b.referenceDigest(namedRef); ok {
		return b.resolveDigest(ref, dgst)
	}

	if dgst, err := b.imageDigestSet.Lookup(refOrID); err == nil {
		return b.resolveDigest(ref, dgst)
	}

	return "", images.ErrImageDoesNotExist{Ref: ref}
}

func (b *legacyGraphdriverImageIdentityBackend) loadRepositories(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var refs struct {
		Repositories map[string]map[string]digest.Digest
	}
	if err := json.NewDecoder(f).Decode(&refs); err != nil {
		return err
	}
	b.repositories = refs.Repositories
	return nil
}

func (b *legacyGraphdriverImageIdentityBackend) loadImageDigests() error {
	entries, err := os.ReadDir(b.imageContent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		dgst := digest.NewDigestFromEncoded(digest.Canonical, entry.Name())
		if err := dgst.Validate(); err != nil {
			continue
		}
		if err := b.imageDigestSet.Add(dgst); err != nil {
			return err
		}
	}
	return nil
}

func (b *legacyGraphdriverImageIdentityBackend) referenceDigest(ref reference.Named) (digest.Digest, bool) {
	if canonical, ok := ref.(reference.Canonical); ok {
		if _, ok := ref.(reference.Tagged); ok {
			var err error
			ref, err = reference.WithDigest(reference.TrimNamed(canonical), canonical.Digest())
			if err != nil {
				return "", false
			}
		}
	} else {
		ref = reference.TagNameOnly(ref)
	}

	repo := b.repositories[reference.FamiliarName(ref)]
	if repo == nil {
		return "", false
	}
	dgst, ok := repo[reference.FamiliarString(ref)]
	return dgst, ok
}

func (b *legacyGraphdriverImageIdentityBackend) resolveDigest(ref reference.Reference, dgst digest.Digest) (image.ID, error) {
	if err := dgst.Validate(); err != nil {
		return "", images.ErrImageDoesNotExist{Ref: ref}
	}
	if _, err := os.Stat(filepath.Join(b.imageContent, dgst.Encoded())); err != nil {
		return "", images.ErrImageDoesNotExist{Ref: ref}
	}
	return image.ID(dgst), nil
}
