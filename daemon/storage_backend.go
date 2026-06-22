package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/containerd/log"
	"github.com/moby/moby/v2/daemon/container"
	"github.com/moby/moby/v2/daemon/internal/layer"
	"github.com/moby/moby/v2/daemon/storagebackend"
	"github.com/pkg/errors"
)

func (daemon *Daemon) initStorageRouter(ctx context.Context, cfg *configStore, containers map[string]map[string]*container.Container, currentDriver string) error {
	router, err := storagebackend.NewRouter(imageServiceStorageBackend{imageService: daemon.imageService})
	if err != nil {
		return err
	}

	for driver, driverContainers := range containers {
		if driver == "" || driver == currentDriver || len(driverContainers) == 0 {
			continue
		}

		layerStore, err := layer.NewStoreFromOptions(layer.StoreOptions{
			Root:               cfg.Root,
			GraphDriver:        driver,
			GraphDriverOptions: cfg.GraphOptions,
			IDMapping:          daemon.idMapping,
		})
		if err != nil {
			log.G(ctx).WithFields(log.Fields{
				"driver":    driver,
				"container": len(driverContainers),
				"error":     err,
			}).Warn("legacy storage backend is not available; containers using it will be restored without RWLayer")
			continue
		}

		backend := graphdriverStorageBackend{
			driverName: driver,
			layerStore: layerStore,
		}
		if err := router.RegisterLegacy(backend); err != nil {
			return err
		}
		log.G(ctx).WithFields(log.Fields{
			"driver":    driver,
			"container": len(driverContainers),
		}).Info("registered legacy storage backend for previously-created containers")
	}

	daemon.storageRouter = router
	return nil
}

func flattenContainerGroups(groups map[string]map[string]*container.Container) map[string]*container.Container {
	containers := make(map[string]*container.Container)
	for _, group := range groups {
		for id, ctr := range group {
			containers[id] = ctr
		}
	}
	return containers
}

func (daemon *Daemon) getContainerRWLayer(ctr *container.Container) (container.RWLayer, error) {
	if daemon.storageRouter == nil {
		return daemon.imageService.GetLayerByID(ctr.ID)
	}
	ref := &storagebackend.ContainerRef{
		ID:     ctr.ID,
		Driver: ctr.Driver,
	}
	if err := daemon.storageRouter.RestoreLayer(ref); err != nil {
		return nil, err
	}
	return ref.RWLayer, nil
}

func (daemon *Daemon) releaseContainerRWLayer(ctr *container.Container, rwLayer container.RWLayer) error {
	if daemon.storageRouter == nil {
		return daemon.imageService.ReleaseLayer(rwLayer)
	}
	return daemon.storageRouter.ReleaseLayer(&storagebackend.ContainerRef{
		ID:      ctr.ID,
		Driver:  ctr.Driver,
		RWLayer: rwLayer,
	})
}

func (daemon *Daemon) getContainerLayerMountID(ctr *container.Container) (string, error) {
	if daemon.storageRouter == nil {
		return daemon.imageService.GetLayerMountID(ctr.ID)
	}
	return daemon.storageRouter.GetLayerMountID(&storagebackend.ContainerRef{
		ID:     ctr.ID,
		Driver: ctr.Driver,
	})
}

func (daemon *Daemon) cleanupStorageBackends() error {
	if daemon.storageRouter == nil {
		return daemon.imageService.Cleanup()
	}
	return daemon.storageRouter.Cleanup()
}

type imageServiceStorageBackend struct {
	imageService ImageService
}

func (b imageServiceStorageBackend) BackendID() storagebackend.BackendID {
	return storagebackend.BackendID(b.imageService.StorageDriver())
}

func (b imageServiceStorageBackend) GetLayerByID(containerID string) (storagebackend.RWLayer, error) {
	return b.imageService.GetLayerByID(containerID)
}

func (b imageServiceStorageBackend) ReleaseLayer(rwLayer storagebackend.RWLayer) error {
	return b.imageService.ReleaseLayer(rwLayer)
}

func (b imageServiceStorageBackend) GetLayerMountID(containerID string) (string, error) {
	return b.imageService.GetLayerMountID(containerID)
}

func (b imageServiceStorageBackend) Cleanup() error {
	return b.imageService.Cleanup()
}

type graphdriverStorageBackend struct {
	driverName string
	layerStore layer.Store
}

func (b graphdriverStorageBackend) BackendID() storagebackend.BackendID {
	return storagebackend.BackendID(b.driverName)
}

func (b graphdriverStorageBackend) GetLayerByID(containerID string) (storagebackend.RWLayer, error) {
	return b.layerStore.GetRWLayer(containerID)
}

func (b graphdriverStorageBackend) ReleaseLayer(rwLayer storagebackend.RWLayer) error {
	l, ok := rwLayer.(layer.RWLayer)
	if !ok {
		return fmt.Errorf("unexpected RWLayer type for graphdriver %q: %T", b.driverName, rwLayer)
	}

	metadata, err := b.layerStore.ReleaseRWLayer(l)
	for _, m := range metadata {
		log.G(context.TODO()).WithField("chainID", m.ChainID).Infof("release legacy RWLayer: cleaned up layer %s", m.ChainID)
	}
	if err != nil && !errors.Is(err, layer.ErrMountDoesNotExist) && !errors.Is(err, os.ErrNotExist) {
		return errors.Wrapf(err, "legacy driver %q failed to remove root filesystem", b.driverName)
	}
	return nil
}

func (b graphdriverStorageBackend) GetLayerMountID(containerID string) (string, error) {
	return b.layerStore.GetMountID(containerID)
}

func (b graphdriverStorageBackend) Cleanup() error {
	return b.layerStore.Cleanup()
}
