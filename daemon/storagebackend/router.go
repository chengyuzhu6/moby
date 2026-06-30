package storagebackend

import (
	"errors"
	"fmt"
	"slices"
)

// BackendKind identifies the storage model that owns a container's rootfs.
type BackendKind string

const (
	// BackendKindGraphDriver is a Docker graphdriver-backed container rootfs.
	BackendKindGraphDriver BackendKind = "graphdriver"

	// BackendKindContainerdSnapshotter is a containerd snapshotter-backed
	// container rootfs.
	BackendKindContainerdSnapshotter BackendKind = "containerd-snapshotter"
)

// BackendID identifies the storage backend that owns a container's rootfs. The
// kind is part of the identity because graphdrivers and containerd snapshotters
// use different metadata models and may have overlapping names.
type BackendID struct {
	Kind BackendKind
	Name string
}

// NewGraphDriverBackendID returns the id for a Docker graphdriver backend.
func NewGraphDriverBackendID(name string) BackendID {
	return BackendID{Kind: BackendKindGraphDriver, Name: name}
}

// NewContainerdSnapshotterBackendID returns the id for a containerd snapshotter
// backend.
func NewContainerdSnapshotterBackendID(name string) BackendID {
	return BackendID{Kind: BackendKindContainerdSnapshotter, Name: name}
}

func (id BackendID) String() string {
	if id.Kind == "" {
		return id.Name
	}
	if id.Name == "" {
		return string(id.Kind)
	}
	return string(id.Kind) + ":" + id.Name
}

// Valid reports whether id has both a storage kind and backend name.
func (id BackendID) Valid() bool {
	return id.Kind != "" && id.Name != ""
}

// RWLayer is the container writable-layer surface that storage routing needs.
//
// It intentionally matches daemon/container.RWLayer without importing the
// daemon/container package, so this prototype can be tested independently from
// platform-specific daemon code.
type RWLayer interface {
	Mount(mountLabel string) (string, error)
	Unmount() error
	Metadata() (map[string]string, error)
}

// ContainerRef contains the container metadata needed to route storage
// operations. The daemon can adapt its full container type to this shape.
type ContainerRef struct {
	ID        string
	Driver    string
	BackendID BackendID
	RWLayer   RWLayer
}

// ContainerStorageBackend is the minimal container-lifecycle storage surface
// needed to keep containers created by a previous backend manageable.
//
// It intentionally does not include image-management operations. New image
// operations should continue to use the daemon's default ImageService.
type ContainerStorageBackend interface {
	BackendID() BackendID
	GetLayerByID(containerID string) (RWLayer, error)
	ReleaseLayer(RWLayer) error
	GetLayerMountID(containerID string) (string, error)
	Cleanup() error
}

// Router resolves container storage operations to the backend that created the
// container instead of always using the daemon's current default backend.
type Router struct {
	defaultBackend ContainerStorageBackend
	backends       map[BackendID]ContainerStorageBackend
}

// NewRouter creates a router with the backend used for new containers.
func NewRouter(defaultBackend ContainerStorageBackend) (*Router, error) {
	if defaultBackend == nil {
		return nil, errors.New("default storage backend is nil")
	}
	id := defaultBackend.BackendID()
	if !id.Valid() {
		return nil, errors.New("default storage backend has empty id")
	}
	return &Router{
		defaultBackend: defaultBackend,
		backends: map[BackendID]ContainerStorageBackend{
			id: defaultBackend,
		},
	}, nil
}

// RegisterLegacy adds a restore-only backend for containers created before the
// daemon switched to its current default backend.
func (r *Router) RegisterLegacy(backend ContainerStorageBackend) error {
	if backend == nil {
		return errors.New("legacy storage backend is nil")
	}
	id := backend.BackendID()
	if !id.Valid() {
		return errors.New("legacy storage backend has empty id")
	}
	if _, ok := r.backends[id]; ok {
		return fmt.Errorf("storage backend %q is already registered", id)
	}
	r.backends[id] = backend
	return nil
}

// Default returns the backend used for newly created containers.
func (r *Router) Default() ContainerStorageBackend {
	return r.defaultBackend
}

// BackendIDs returns all registered backend IDs in stable order.
func (r *Router) BackendIDs() []BackendID {
	ids := make([]BackendID, 0, len(r.backends))
	for id := range r.backends {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b BackendID) int {
		if a.Kind != b.Kind {
			return cmpString(string(a.Kind), string(b.Kind))
		}
		return cmpString(a.Name, b.Name)
	})
	return ids
}

// Lookup returns a registered backend by id.
func (r *Router) Lookup(id BackendID) (ContainerStorageBackend, bool) {
	backend, ok := r.backends[id]
	return backend, ok
}

// BackendForContainer resolves the backend that owns a container's RW layer.
func (r *Router) BackendForContainer(ctr *ContainerRef) (ContainerStorageBackend, error) {
	if ctr == nil {
		return nil, errors.New("container is nil")
	}
	id := ctr.BackendID
	if !id.Valid() {
		id = r.defaultBackend.BackendID()
	}
	backend, ok := r.Lookup(id)
	if !ok {
		return nil, fmt.Errorf("storage backend %q for container %s is not registered", id, ctr.ID)
	}
	return backend, nil
}

// RestoreLayer loads the container's RW layer from the backend that created it.
func (r *Router) RestoreLayer(ctr *ContainerRef) error {
	backend, err := r.BackendForContainer(ctr)
	if err != nil {
		return err
	}
	rwLayer, err := backend.GetLayerByID(ctr.ID)
	if err != nil {
		return err
	}
	ctr.RWLayer = rwLayer
	return nil
}

// ReleaseLayer releases a container RW layer through the backend that owns it.
func (r *Router) ReleaseLayer(ctr *ContainerRef) error {
	if ctr == nil {
		return errors.New("container is nil")
	}
	if ctr.RWLayer == nil {
		return fmt.Errorf("RWLayer of container %s is unexpectedly nil", ctr.ID)
	}
	backend, err := r.BackendForContainer(ctr)
	if err != nil {
		return err
	}
	return backend.ReleaseLayer(ctr.RWLayer)
}

// GetLayerMountID returns the backend-specific mount ID for a container.
func (r *Router) GetLayerMountID(ctr *ContainerRef) (string, error) {
	backend, err := r.BackendForContainer(ctr)
	if err != nil {
		return "", err
	}
	return backend.GetLayerMountID(ctr.ID)
}

// Cleanup releases resources for every registered storage backend.
func (r *Router) Cleanup() error {
	var errs []error
	for _, backend := range r.backends {
		if err := backend.Cleanup(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
