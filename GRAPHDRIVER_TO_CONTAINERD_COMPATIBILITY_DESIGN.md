# Docker Storage Backend 显式切换后的历史容器兼容方案

## 问题定义

本文关注的问题不是某个具体 snapshotter，而是 Docker daemon 在用户显式切换默认 storage backend 后，历史容器不可见或不可管理的问题。

这里的 backend 可以是：

- 传统 graphdriver image store，例如 `overlay2`、`vfs`。
- containerd image store + containerd snapshotter，例如 `overlayfs`、`native`、`erofs`、`stargz`、`nydus`。

和社区后续 image management 路线最一致的核心场景是：

- graphdriver image store -> containerd image store。
- containerd image store 中从一个 snapshotter 切到另一个 snapshotter。

典型场景包括：

- `overlay2` 切换到 containerd `overlayfs`。
- `vfs` 切换到 containerd `native`。
- containerd `overlayfs` 切到其他 containerd snapshotter，例如 `erofs`、`stargz`、`nydus` 等。

反向切换到 graphdriver 或 graphdriver 之间互切可以复用部分机制，但不应作为第一阶段目标。社区长期方向是让 image management 收敛到 containerd image store，而不是继续扩展 graphdriver image store 的长期能力。

当前上游 master 已经具备 containerd image store，并且新安装场景可以默认使用 containerd snapshotter。但是 daemon 在启动时仍然选择一个当前 storage backend，并只恢复该 backend 对应的容器。

用户期望的是：

- 切换默认 backend 后，新镜像和新容器使用新的默认 backend。
- 切换前已有的历史容器继续出现在 Docker 管理视图中。
- 旧容器可以继续 `inspect`、`start`、`stop`、`restart`、`rm`。
- 不要求旧 backend 和新 backend 完整双写共存。

最终方案一句话描述：

> Add a restore-only compatibility path for containers created with a previous storage backend after an explicit backend switch. Image management continues to use the selected default backend.

## 当前方案定位

当前提出的方案不是“多个 image store 完整共存”，而是：

**restore-only legacy container storage compatibility**

也就是：

- 新 backend 是唯一默认写入后端。
- 旧 backend 只用于切换前已经存在的容器。
- 旧 backend 的 container RW layer 或 active snapshot 允许受限管理。
- 不允许通过旧 backend 创建新容器或写入新镜像。
- 旧 backend 的 image metadata 不进入默认 image workflow；如后续展示，也只作为只读 inventory 或迁移输入。

这个方案不应被定位为新的多后端 image management 架构，而应被定位为 Docker storage backend 演进过程中的 backwards compatibility boundary：

- Docker 仍然只有一个默认写入 backend。
- 历史容器按照创建时记录的 backend 恢复和管理。
- 旧 backend 不参与新工作负载调度，也不与新 backend 形成完整多活。
- 兼容层的职责是保证 backend 切换不破坏已有容器的可管理性。

换句话说，它不是“长期多活存储架构”，也不是 image store migration 的替代品。它是 daemon 对历史容器元数据和 RW layer 的 backward compatibility 机制。

## 与上游镜像管理演进方向的关系

这个方案必须顺着 Moby/Docker Engine 后续 image management 的方向设计，而不是把 graphdriver image store 重新扩展成长期同等能力的第二套 image backend。

上游当前方向可以概括为：

- fresh install 默认走 containerd image store。
- upgrade 场景如果已有 graphdriver 状态，默认继续使用 legacy graphdriver，避免自动隐藏已有数据。
- 用户显式启用 containerd image store 后，旧 graphdriver 镜像和容器仍留在磁盘上，但当前行为是从 Docker 管理视图隐藏。
- 自动迁移仍是实验性能力，并且只在没有 running/stopped 容器等受限条件下尝试。
- containerd image store 是后续镜像能力的主要承载点，包括 multi-platform image、attestation、Wasm workloads、advanced snapshotter 等。

因此，本方案不能改变这些上游语义：

- 不改变 `graphdriver-prior` 保护。已有 graphdriver 状态时，daemon 默认仍应继续使用 graphdriver。
- 不扩大 automatic migration 的触发条件。尤其不能在存在旧容器时自动迁移 RW layer。
- 不把 legacy graphdriver image store 重新变成 active writable image store。
- 不让 legacy image 参与 `docker run`、pull、build、load、tag、push 的默认解析或写入路径。
- 不阻碍 containerd image store 成为长期默认 image management backend。

本方案补齐的是一个更窄的缺口：

```text
用户显式切换 default backend 后，
旧 backend 上已经存在的容器不应从 Docker 管理视图中消失，
但旧 backend 仍然只作为受限的 legacy container storage backend。
```

这意味着实现上要优先保护上游未来计划：

- default `ImageService` 继续代表当前选中的 image management backend，长期会越来越偏向 containerd。
- legacy 兼容逻辑应尽量放在 container lifecycle / RW layer routing 层，而不是复制或冻结整个旧 image service。
- legacy image read-only view 如果实现，也只能作为观测和迁移辅助，不应影响 default image resolution。
- 对外表述应避免 “support multiple image stores”，改为 “keep previously-created containers manageable after an explicit backend switch”。

### 面向社区后续计划的兼容路线

为了避免和社区后续 image management 方案分叉，本方案应拆成三个互不越界的问题：

```text
image management future:
  default path converges on containerd image store

legacy container compatibility:
  keep old containers manageable after explicit backend switch

migration assistance:
  help users move data into the default containerd-backed world
```

这三个问题不能混在一起：

- image management 的长期方向是 containerd image store，不应为了兼容旧容器而让 graphdriver image store 继续承担新镜像能力。
- legacy container compatibility 只解决“旧容器可管理”，不解决“旧镜像继续像新镜像一样参与所有 API”。
- migration assistance 应把旧容器或旧镜像转换到 default backend，而不是让两个 backend 长期对等共存。

更符合社区方向的推进方式应是：

1. 保持现有默认行为：升级用户继续停留在 graphdriver，fresh install 使用 containerd image store。
2. 当用户显式切换 backend 时，不再把旧容器静默隐藏，而是通过 restore-only compatibility 让旧容器以 legacy 状态出现。
3. 新 pull/build/load/import/tag/push 全部进入当前 default image store。
4. 旧容器如果需要迁移，应通过 `export`、`commit`、recreate 或未来专门 migration command 进入 default backend。
5. legacy image 只作为 read-only inventory 或迁移输入出现，不作为普通 image resolver 的候选。
6. 当 graphdriver 逐步退出主路径时，legacy graphdriver adapter 可以保持最小 surface，最终只承担删除旧容器、导出旧数据、提示用户迁移的职责。

因此，本方案中所有 API 设计都应遵守一个原则：

```text
default backend owns image management;
legacy backend owns only the RW layer of containers that were created there.
```

这也决定了实现边界：

- `daemon.ImageService` 不应被改造成完整 multi-store aggregator。
- 新增的 `ContainerStorageRouter` 应是 compatibility layer，而不是新的 image management abstraction。
- containerd snapshotter -> snapshotter 的兼容应优先使用 containerd per-snapshotter 能力，而不是按 graphdriver legacy 模型实现。
- graphdriver -> containerd 的兼容应尽量限制在 graphdriver layer store 的 restore/release/export 能力上。
- 后续如果社区增强 automatic migration，本方案应让 migration 可以消费 legacy backend，但不要求 migration 依赖 legacy backend 永久存在。

从社区提案角度，这个方案可以表达为现有计划的下一步：

```text
Today:
  explicit backend switch hides containers from the other backend.

Proposed:
  explicit backend switch keeps those containers visible and manageable
  through a restricted container-storage compatibility path,
  while all image management continues to use the selected default backend.
```

这样它不是一条分叉路线，而是把上游文档中“旧数据仍在磁盘上但会隐藏”的行为，收敛为“旧容器可见、可停止、可启动、可删除、可迁移；旧镜像仍不进入 default image workflow”。

## 为什么不是兼容旧 image store

旧容器不只依赖旧 image metadata。它真正影响 `start`、`stop`、`rm` 的，是创建它时所用 backend 的 writable layer 或 active snapshot 管理能力。

一个历史容器至少涉及：

- 镜像 lower layers。
- 容器自己的 RW layer。
- `container.Driver` 字段。
- 对应 backend 的 mount/unmount 状态。
- layer reference 和 release 逻辑。

因此，第一阶段不应把目标定义为“兼容旧 image store”。更准确的目标是“兼容旧容器的 storage backend”。为了让旧容器继续被 Docker 管理，legacy backend 至少要支持：

- daemon restore 时恢复 `RWLayer`。
- `docker start` 时 mount rootfs。
- `docker stop` 时 unmount rootfs。
- `docker rm` 时 release/remove RW layer。
- `docker inspect` 返回旧 driver 信息。
- 后续可选支持 `diff`、`export`，以及作为 migration assistance 的 `commit`。

所以更准确的边界是：

```text
legacy image metadata: not part of default image workflow
legacy image layers: read-only dependency of old containers
legacy container RW layer: restricted mutable
legacy backend: no new image/container writes
```

## 上游 master 当前行为

上游 master 在 daemon 启动时已经会加载所有容器，并按 `container.Driver` 分组。问题在于最终只恢复当前 `driverName` 对应的一组。

简化后逻辑如下：

```go
containers, err := d.loadContainers(ctx)
driverName := getDriverOverride(ctx, cfgStore.GraphDriver, imgStoreChoice)
driverContainers, ok := containers[driverName]
if err := d.restore(ctx, cfgStore, driverContainers); err != nil {
	return nil, err
}
```

也就是说：

- `container.Driver == driverName` 的容器会恢复。
- 其他 driver 的容器会被记录日志，但不会进入 daemon 管理视图。

这导致默认 backend 切换后，旧 backend 创建的容器不可见或不可管理。

## 建议架构

### Backend Identity

第一阶段不能只用 `container.Driver` 作为路由键。`container.Driver` 记录的是后端名称，例如 `overlay2`、`overlayfs`、`native`、`btrfs`，但它不表达这个名称属于哪一种存储模型。

需要显式区分：

```go
type StorageBackendKind string

const (
    BackendGraphDriver StorageBackendKind = "graphdriver"
    BackendContainerd  StorageBackendKind = "containerd"
)

type StorageBackendID struct {
    Kind StorageBackendKind
    Name string // graphdriver name or containerd snapshotter name
}
```

原因：

- `overlay2` 是 Docker graphdriver，`overlayfs` 是 containerd snapshotter，名称接近但语义不同。
- `btrfs`、`zfs`、`devmapper` 等名称可能同时出现在 graphdriver 或 snapshotter 生态里。
- 仅凭 `container.Driver` 无法区分“旧 graphdriver 容器”和“旧 containerd snapshotter 容器”。

兼容策略：

- 新创建容器继续写入当前 `container.Driver`，保持历史 metadata 兼容。
- 兼容模式内部使用 `StorageBackendID` 做路由。
- 对旧 metadata，如果没有显式 kind，启动时根据 daemon 选择历史、磁盘布局、是否存在 graphdriver layerdb、是否存在 containerd snapshot metadata 做一次 conservative inference。
- 如果 inference 不唯一，不应猜测启动；应报告 backend ambiguous，并要求用户显式配置 legacy backend。

长期看，可以在容器 metadata 中增加非破坏性字段，例如：

```json
{
  "Driver": "overlay2",
  "StorageBackend": {
    "Kind": "graphdriver",
    "Name": "overlay2"
  }
}
```

旧 daemon 会忽略新增字段，新 daemon 可以优先使用该字段。

### Storage Backend Router

引入一个后端路由层：

```text
Daemon
  └── StorageBackendRouter
        ├── default backend
        │     └── selected image store + selected storage backend
        └── legacy backends
              └── previous container storage backend
```

路由原则：

- 新建容器、pull、build、load 默认走 default backend。
- 已存在容器根据 `StorageBackendID` 路由。
- legacy backend 只服务已有容器。
- 未识别 backend 的容器不应静默消失，应进入 unavailable/degraded 状态或使 daemon 启动失败。

Router 不应只包住 image API。现有 daemon 中很多容器生命周期路径通过全局 `daemon.imageService` 访问 RW layer，因此至少需要拆出一个 container storage resolver：

```go
type ContainerStorageBackend interface {
    BackendID() StorageBackendID
    GetLayerByID(containerID string) (container.RWLayer, error)
    ReleaseLayer(container.RWLayer) error
    GetLayerMountID(containerID string) (string, error)
    Changes(ctx context.Context, ctr *container.Container) ([]archive.Change, error)
    GetContainerLayerSize(ctx context.Context, containerID string) (int64, int64, error)
    Cleanup() error
}
```

这样 `docker start`、`docker stop`、`docker rm`、`docker diff`、`docker export` 等路径可以按容器路由，而不是继续依赖全局 default image service。

当前 demo 已实现这个路由核心：

```text
daemon/storagebackend/router.go
daemon/storagebackend/router_test.go
```

demo 中的 `Router` 提供：

- `Default()`：返回新容器使用的默认 backend。
- `RegisterLegacy()`：注册只服务历史容器的 legacy backend。
- `BackendForContainer()`：根据容器记录的 driver/backend 选择 backend。
- `RestoreLayer()`：从容器所属 backend 恢复 RW layer。
- `ReleaseLayer()`：通过容器所属 backend 释放 RW layer，避免误调用 default backend。

为了让 demo 可以独立编译测试，`daemon/storagebackend` 包仍使用轻量 `ContainerRef` 和 `RWLayer` 接口，没有直接 import daemon 的完整 `container.Container` 类型。

当前已完成一个最小端到端接入：

```text
daemon/storage_backend.go
daemon/daemon.go
daemon/delete.go
```

接入点包括：

- daemon 启动时创建 `storageRouter`，default backend 包装当前 `daemon.imageService`。
- 如果磁盘上存在非当前 driver 的容器，尝试按该 driver 初始化 legacy graphdriver `layer.Store`，并加载对应的只读 `imageStore/referenceStore` 用于 legacy image ref 解析。
- restore 阶段不再只恢复当前 driver 分组的容器，而是恢复所有已加载容器，并通过 router 按 `container.Driver` 恢复 RW layer。
- `docker rm` 释放 RW layer 时通过 router 路由，避免旧容器误调用当前 default backend 的 `ReleaseLayer`。

当前 demo 的端到端范围是：

- 支持 stopped graphdriver legacy 容器在切换到 containerd image store 后继续被 `ps/inspect/start/stop/rm` 管理。
- `docker ps` 展示 legacy 容器时，会通过容器所属 legacy backend 的只读 image resolver 校验 image ref，避免用当前 default image store 查找失败后误退化成 image ID。
- 不改变新建容器、pull、build、load 的 backend 选择；这些仍由当前 default `ImageService` 负责。
- 当前没有实现完整 legacy image store 聚合；`docker images`、`docker image inspect`、`docker ps --filter ancestor=...` 等镜像视图和镜像过滤仍主要使用当前 default image store。
- legacy backend 初始化失败时，daemon 会继续启动并记录 warning；容器仍会被加载，但没有 RWLayer 的容器无法正常 start。这是 demo 行为，正式方案应在 fail-fast 和 degraded mode 之间做显式配置选择。
- 反向方向（containerd snapshotter legacy -> graphdriver default）以及 snapshotter -> snapshotter 切换尚未接入 legacy backend 初始化。

建议分成两个层次：

- `ImageService`: 仍代表 default backend，负责新镜像、新容器、pull、build、load、push。
- `ContainerStorageRouter`: 负责历史容器的 RW layer 恢复、mount、unmount、release 和可选 diff/export。

第一阶段应避免实现完整的 image-service 聚合器；否则容易把问题重新扩大成多 image store 共存。

### Restore 路由

将现有单 driver restore 调整为按 backend 分发：

```text
loadedContainers := d.loadContainers(ctx)
for _, c := range loadedContainers {
    backendID := resolveContainerBackend(c)
    backend := storageRouter.Lookup(backendID)
    if backend == nil {
        reportUnavailable(c, backendID)
        continue
    }
    rwlayer, err := backend.GetLayerByID(c.ID)
    if err != nil {
        reportLayerUnavailable(c, backendID, err)
        continue
    }
    c.RWLayer = rwlayer
    restoreContainer(c)
}
```

第一阶段可以只支持一个 legacy backend，例如 `overlay2`。

重要约束：

- restore 不能只把旧容器放回 container store，还必须用对应 backend 恢复 `RWLayer`。
- `docker rm` 必须用创建该 `RWLayer` 的 backend 做 `ReleaseLayer`。
- `docker start` 在生成 containerd container metadata 时，必须按容器 backend 决定是否设置 `Snapshotter/SnapshotKey`，不能继续只看 daemon 级 `UsesSnapshotter()`。
- containerd snapshotter -> snapshotter 切换时，`GetLayerByID` 必须使用旧容器记录的 snapshotter，而不是当前默认 snapshotter。

### ImageService 与兼容层边界

上游 master 已有统一的 `daemon.ImageService` interface，但这个 interface 应继续代表 default image management backend。为了和社区后续 containerd image store 方向保持一致，第一阶段不应把它改造成完整的 multi-store aggregator。

需要特别注意 `docker ps` 的 `IMAGE` 字段。现有 `refreshImage()` 会用全局 `daemon.imageService.GetImage(ctx, s.Image, ...)` 判断容器创建时的 image ref 是否仍指向原 `ImageID`。切换 backend 后，legacy 容器引用的 image ref 可能只存在于旧 backend 的 image store 中，当前 default image store 查不到它，于是 `refreshImage()` 会把 `IMAGE` 回退成镜像 ID。

demo 当前处理是：`refreshImage()` 不再直接调用全局 default `imageService`，而是通过 storage router 按容器所属 backend 解析 image ref。legacy graphdriver backend 会加载旧 driver 目录下的 `image/<driver>/imagedb` 和 `image/<driver>/repositories.json`，只读解析 ref 是否仍指向容器创建时记录的 `ImageID`。如果 legacy resolver 也无法解析，才回退成 image ID。

这解决了 `docker ps` 的 `IMAGE` 展示问题，但仍不是完整 multi image store。后续如果要支持 `ancestor` filter、`image inspect` 或只读 legacy image 展示，需要把 read-only image reference resolver 扩展成更完整的 backend-aware image inventory API。

更可控的分阶段方式是：

1. 只解决容器 restore 和 RW layer 生命周期路由。
2. 再做 legacy image 的只读 inspect/list。
3. 再做 diff/export/commit 等扩展。
4. 最后考虑 legacy cleanup/prune。

如果后续确实需要 legacy image read-only view，也应优先通过单独的 backend-aware inventory API 或内部辅助接口实现，而不是让 `docker run`、build、pull、load 等 default image workflow 同时查询多个 image store。

## 操作语义

| 操作 | 新 backend 容器 | 旧 backend 容器 | 建议策略 |
| --- | --- | --- | --- |
| `docker create` | 支持 | 不支持 | 新容器只走 default backend |
| `docker run` | 支持 | 不支持 | image 解析默认只使用 default backend |
| `docker ps -a` | 支持 | 支持 | 聚合已恢复容器 |
| `docker inspect <container>` | 支持 | 支持 | 按容器 backend 展示 storage 信息 |
| `docker start` | 支持 | 支持 stopped 容器 | 按 `StorageBackendID` mount，并按容器 backend 设置 containerd metadata |
| `docker stop` | 支持 | 支持 | 通过容器自己的 `RWLayer` unmount |
| `docker rm` | 支持 | 支持 | 通过创建 `RWLayer` 的 backend release，不能调用 default backend release |
| `docker logs` | 支持 | 支持 | 与 image store 弱相关 |
| `docker exec` | 支持 | Phase 1 不承诺 | 依赖 runtime 状态、用户解析和 containerd metadata |
| `docker diff` | 支持 | Phase 1 不承诺 | 需要 legacy backend diff/changes 能力 |
| `docker export` | 支持 | Phase 1 不承诺 | 需要旧 rootfs mount/read 和稳定 unmount 语义 |
| `docker commit` | 支持 | Phase 1 不承诺 | 旧容器 commit 到新后端需单独设计 |
| `docker image ls` | 支持 | 可选只读展示 | legacy image 必须标记来源 |
| `docker image rm` | 支持 | 初期不支持 legacy | 避免误删旧容器依赖 |
| `docker system prune` | 支持 | 初期跳过 legacy | 避免误删 |
| `docker system df` | 支持 | 可选统计 | 需要多 backend usage 聚合 |

Phase 1 的最小可交付语义应定义为：

- 已停止 legacy 容器在 backend 切换后仍出现在 `docker ps -a`。
- `docker inspect <legacy-container>` 返回清晰的 storage backend 信息。
- `docker start/stop/restart <legacy-container>` 使用 legacy backend 的 RW layer。
- `docker rm <legacy-container>` 只删除该容器自己的 metadata 和 RW layer，不删除 legacy image/lower layers。
- 新建容器、pull、build、load、tag、push 全部只使用 default backend。

暂不承诺：

- 对切换前正在运行的 legacy 容器做 live-restore reconnect。
- 对 legacy 容器执行 `exec --user` 的所有语义。
- legacy image 出现在普通 image 解析路径中。
- 旧容器 `commit` 直接写入新 backend。

## 为什么 containerd 更容易支持多后端

这里需要区分两个概念：

- containerd 不一定是“多个 image store 完整共存”。
- containerd 更天然支持“一个 image/content store 配合多个 snapshotter backend”。

它能做到这一点，主要因为架构上把镜像内容、镜像元数据、快照、容器元数据拆开了。

### 1. Content store 与 snapshotter 解耦

containerd 的 content store 按 digest 存储 OCI content，包括 index、manifest、config 和 compressed layers。

这些内容与具体 snapshotter 无关。一个镜像的 blobs 可以只存一份，然后被不同 snapshotter unpack 成不同的 snapshot tree。

containerd 文档中描述的流程是：

1. image content 进入本地 content store。
2. layers 被 unpack 到某个 snapshotter，形成 committed snapshots。
3. 容器运行前，在最终 snapshot 上创建 active snapshot。

这个模型天然允许：

- 同一个 image content 被 `overlayfs` unpack。
- 同一个 image content 也被 `native`、`btrfs`、`erofs` 等 snapshotter unpack。
- content store 共享，snapshot storage 分离。

Docker graphdriver 模型则不同。传统 Docker 的 image metadata、layerdb、graphdriver cache-id、driver 目录强绑定在 `image/<driver>` 和 `<driver>` 数据目录下。

### 2. Snapshotter 是插件化服务

containerd snapshotter 是插件，按名称访问：

```text
SnapshotService("overlayfs")
SnapshotService("native")
SnapshotService("btrfs")
SnapshotService("erofs")
```

不同 snapshotter 有自己的存储根目录，例如：

```text
/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs
/var/lib/containerd/io.containerd.snapshotter.v1.native
/var/lib/containerd/io.containerd.snapshotter.v1.erofs
```

因此 containerd 的客户端可以在操作时显式传入 snapshotter 名称，而不是依赖 daemon 级唯一 graphdriver。

### 3. Container 元数据记录 Snapshotter

containerd 的 container metadata 中有 `Snapshotter` 字段：

```go
// Snapshotter specifies the snapshotter name used for rootfs
//
// This field is not required but immutable.
Snapshotter string
```

这意味着 containerd 的设计允许每个 container 记录自己使用的 snapshotter。启动 task 时可以根据 container 的 `Snapshotter` 和 `SnapshotKey` 找到对应 snapshot service。

这正是 Docker 当前缺失的关键能力之一。Docker 的 container metadata 也有 `Driver` 字段，但 daemon restore 逻辑仍然只恢复当前全局 driver 对应的一组容器，而不是为每个 driver 初始化对应 backend 并分发恢复。

### 4. Image metadata 不必绑定单一 snapshotter

containerd image 记录主要是 name 到 descriptor target 的映射。它描述“这个 image reference 指向哪个 OCI descriptor”，而不是“这个 image 必须属于某个 snapshotter”。

是否已经 unpack 到某个 snapshotter，是另一个维度。一个 image 可以：

- content 已存在，但未 unpack。
- 已 unpack 到 `overlayfs`。
- 也可以再 unpack 到 `native` 或其他 snapshotter。

这使得“镜像内容”和“运行时 rootfs backend”之间不是强绑定关系。

### 5. Namespace 提供多租户隔离

containerd API 是 namespaced 的。不同 consumer 可以在不同 namespace 中有独立 image names 和 container names，而底层 content 仍按 digest 共享。

这不是本方案的核心，但说明 containerd 从设计上就把 metadata scope、content sharing、runtime resources 分开处理。

## Docker 当前为什么没有直接获得 containerd 的多 snapshotter 能力

虽然 containerd 可以支持多个 snapshotter，但 Docker daemon 当前没有完整暴露这个能力，原因包括：

### 1. Docker ImageService 仍选择一个默认 backend

上游 master 启动时会选择 graphdriver image service 或 containerd image service。即使使用 containerd image service，也会记录一个默认 snapshotter：

```go
Snapshotter: driverName
```

很多 Docker image/container 操作默认使用这个 snapshotter。

### 2. 容器 restore 仍按当前 driver 过滤

master 已经把容器按 driver 分组，但 restore 时只处理当前 `driverName`。

这说明 Docker 还没有把 containerd 的 per-container snapshotter 模型完整映射到 daemon 的容器恢复路径。

### 3. Docker API 历史上是单 storage-driver 语义

`docker info`、`GraphDriver`、`DriverStatus`、`docker image ls`、`docker system df`、`prune` 等 API 都长期假设 daemon 有一个主要 storage backend。

如果引入多 backend，需要重新定义：

- `Storage Driver` 显示什么。
- `docker run <name>` 从哪个 backend 解析 image。
- 同名 image reference 冲突如何处理。
- `docker image rm` 删除哪个 backend 的 image。
- `prune` 是否跨 backend 清理。

### 4. Graphdriver 和 containerd snapshotter 是两套模型

在 containerd 内多个 snapshotter 之间切换，至少共享 content store 和 image metadata 模型。

但 graphdriver 到 containerd image store 的兼容更复杂，因为二者差异包括：

- graphdriver layerdb vs containerd metadata/content/snapshot store。
- Docker reference store vs containerd image store。
- graphdriver cache-id vs containerd snapshot key。
- Docker RWLayer abstraction vs containerd snapshot active key。

因此，本方案不是简单启用 containerd 多 snapshotter，而是要在 Docker daemon 中保留“历史 backend 兼容层”。当历史 backend 是 graphdriver 时，需要兼容 graphdriver layer store；当历史 backend 是 containerd snapshotter 时，需要按容器记录的 snapshotter 找回对应 snapshot service。

## 推荐实现阶段

### Phase 1: Legacy Container Restore

目标：

- 新 backend 是当前显式选择的 default backend，社区主路径是 containerd image store。
- 引入 `StorageBackendID` 和 `ContainerStorageRouter`。
- legacy backend 只恢复切换前已经存在的 stopped 容器。
- 支持旧容器 `ps`、`inspect`、`start`、`stop`、`restart`、`logs`、`rm`。
- `restore`、`start`、`rm` 不再通过全局 default image service 处理 legacy RW layer。

不支持：

- legacy image 写入。
- legacy image prune。
- legacy image 参与新容器创建。
- 多个 legacy backend。
- running legacy 容器的 live-restore reconnect。
- `exec`、`diff`、`export`、`commit` 的完整兼容。

退出条件：

- graphdriver `overlay2` 创建 stopped 容器后，切到 containerd `overlayfs`，旧容器可以 `ps/inspect/start/stop/rm`。
- containerd `overlayfs` 创建 stopped 容器后，切到 containerd `native`，旧容器可以通过原 snapshotter 恢复 RW layer。
- 删除 legacy 容器时，不删除 legacy image/lower layers，也不调用 default backend 的 release 逻辑。
- legacy backend 初始化失败时，不静默隐藏容器。

### Phase 2: Optional Legacy Inventory

目标：

- 可选支持 legacy image inspect 或 inventory。
- 可选支持 `docker image ls` 以明确标记 backend 的方式展示 legacy image。
- 明确标记 backend 来源。
- 避免同名 tag 影响新容器创建。

约束：

- legacy image 只读展示不能进入 `docker run <tag>` 的默认解析路径。
- 同名 tag 同时存在时，`docker image ls` 必须能表达来源 backend，否则应先不展示 legacy image tag。
- `docker image rm` 默认只作用于 default backend；如果未来允许 legacy image 删除，需要显式 backend-aware 参数或安全交互。

### Phase 3: Migration Assistance

目标：

- 支持旧容器 export。
- 支持旧容器 commit 到新 containerd backend。
- 支持用户逐步迁移或重建旧容器。

约束：

- `commit` 不应把 legacy image store 变成可写后端；它应该读取旧容器 rootfs diff，然后在 default backend 创建新 image。
- graphdriver -> containerd 的 commit/export 需要处理 whiteout、xattr、idmap、mount label 和平台 metadata。
- containerd snapshotter -> snapshotter 的迁移可以复用 content store，但仍需要确认目标 snapshotter 已 unpack 或可 unpack。
- migration assistance 的目标是减少 legacy backend 依赖，而不是扩大 legacy backend 能力。

### Phase 4: Cleanup

目标：

- 支持安全清理不再被旧容器引用的 legacy image/layer。
- 支持 backend-aware disk usage。
- 支持 backend-aware prune。

约束：

- prune 必须先基于所有 legacy 容器引用建立保护集。
- `system df` 需要按 backend 分组展示，避免把 default backend 和 legacy backend 的空间混在一个总数中误导用户。
- cleanup 必须保证默认行为保守；任何跨 backend 删除都应可审计、可测试。

## 核心缺陷和风险

### Backend identity 不完整

只使用 `container.Driver` 会产生歧义。方案必须引入或推断 `StorageBackendID{Kind, Name}`。

风险点：

- 老容器 metadata 里没有 backend kind。
- 同名 backend 可能属于不同模型。
- 用户连续切换 backend 后，仅支持一个 legacy backend 会让更早的容器再次不可管理。

建议：

- 第一阶段只支持一个明确配置或明确推断的 legacy backend。
- 如果推断结果不唯一，daemon 应失败启动或进入显式 degraded mode。
- 新创建容器开始写入扩展 metadata，为后续多次切换打基础。

### 全局 imageService 依赖

现有代码中 `restore`、`rm`、`start`、`diff`、`export` 等路径仍然通过全局 `daemon.imageService` 访问 RW layer 或 storage driver。

风险点：

- `restore` 用 default backend 查旧容器 RW layer，会导致旧容器注册但无法启动。
- `rm` 用 default backend release legacy RW layer，可能类型不匹配或释放错误资源。
- `start` 用 daemon 级 `UsesSnapshotter()` 和 default `StorageDriver()` 设置 containerd metadata，可能把 graphdriver 容器错误标记为 snapshotter 容器，或把旧 snapshotter 容器指向新 snapshotter。

建议：

- 引入 `ContainerStorageRouter`，并把 `GetLayerByID`、`ReleaseLayer`、`GetLayerMountID`、`Changes` 等容器 layer 操作从 image API 中拆出来。
- `container.RWLayer` 的创建者和释放者必须一致。
- OCI/containerd metadata 生成必须按容器 backend，而不是按 daemon default backend。

### API 语义复杂化

一旦新旧容器同时存在，单一 `Storage Driver` 语义就不再完全准确。需要在 `docker info` 和 `inspect` 中清楚表达默认 backend 与 legacy backend。

### 删除风险

`docker image rm`、`docker container rm`、`docker system prune` 是最容易造成数据损坏的路径。初期必须保守：

- 禁止 legacy image 删除。
- 禁止 legacy image prune。
- 只允许删除 legacy container 自己的 RW layer。

### Degraded 状态语义

“旧 backend 不可用但容器不消失”需要明确用户可见行为，否则实现容易变成另一种静默失败。

建议：

- 如果默认策略是 fail-fast，则存在 legacy 容器但 backend 初始化失败时，daemon 启动失败，并提示缺失 backend。
- 如果支持 degraded mode，则 `docker ps -a` 和 `docker inspect` 必须能显示容器 backend unavailable。
- degraded 容器至少应允许 `docker rm` 删除 Docker metadata，但是否删除磁盘 layer 取决于 backend 是否可访问；不能假装已清理完整。

### Image reference 冲突

同名 tag 可能同时存在于 legacy backend 和新 backend。

建议：

- 新容器 image 解析只使用 default backend。
- legacy image 不参与新容器创建。
- legacy image 如展示，必须标记 backend。

### Backend 初始化失败

如果存在旧 backend 容器，但 legacy backend 初始化失败，不应静默隐藏旧容器。

建议：

- 默认让 daemon 启动失败，提示 legacy backend 初始化失败。
- 或提供 degraded mode，但必须在日志和 `docker info` 中明确展示。

### Live-restore 和 running 容器

running 容器比 stopped 容器复杂得多。它涉及已存在 task、rootfs mount、containerd container metadata、runtime reconnect 和网络恢复。

建议：

- Phase 1 明确不支持 running legacy 容器的 backend 切换 reconnect。
- 如果发现 legacy 容器处于 running 状态，默认应 fail-fast 或标记为 unsupported degraded，而不是尝试半恢复。
- live-restore 支持应作为独立阶段设计和测试。

### 安全与隔离配置

legacy backend 初始化不是单纯打开一个目录，还要保证 idmap、SELinux/AppArmor、mount label、rootless、userns-remap 等配置与容器创建时兼容。

建议：

- backend router 初始化时校验当前 daemon 的 idmapping/rootless/SELinux 约束是否能安全挂载旧 layer。
- 不兼容时不要自动 mount，应给出明确错误。
- `exec --user` 之类依赖 rootfs 用户数据库的路径，在 graphdriver 与 snapshotter 混用时需要单独验证。

### 维护成本

这是社区最可能担心的问题。方案需要避免变成长期完整双栈。对外应强调：

- 这是迁移兼容模式。
- legacy backend 是受限能力。
- 目标是为 storage backend 演进提供稳定的历史容器兼容性。

## 社区视角下的可接受表达

不建议表述为：

> Support multiple image stores in Docker.

更建议表述为：

> Keep containers created with the previous storage backend manageable after switching Docker daemon to a new storage backend.

或者：

> Add a restore-only compatibility mode for containers created with the previous storage backend. New images and containers continue to use the currently configured backend.

这种表述更符合上游已有讨论方向，也更容易避开“完整多后端共存”的复杂承诺。

## 当前 demo 手动验证步骤

当前接入代码需要在 Linux 环境验证。macOS 上 daemon 包依赖 Linux-only graphdriver/network/containerfs 实现，不能直接运行 daemon 端到端测试。

建议使用独立 `data-root` 和 `exec-root`，避免影响宿主机已有 Docker 数据：

```bash
ROOT=/tmp/moby-legacy-router-root
EXEC=/tmp/moby-legacy-router-exec
PID=/tmp/moby-legacy-router.pid
SOCK=/tmp/moby-legacy-router.sock
CFG=/tmp/moby-legacy-router-daemon.json
rm -rf "$ROOT" "$EXEC" "$PID" "$SOCK" "$CFG"
```

1. 使用 graphdriver 启动 daemon，并创建旧容器：

```bash
dockerd \
  --debug \
  --host "unix://$SOCK" \
  --pidfile "$PID" \
  --data-root "$ROOT" \
  --exec-root "$EXEC" \
  --iptables=false \
  --ip6tables=false \
  --storage-driver=overlay2

docker -H "unix://$SOCK" run --name old-overlay2 busybox sh -c 'echo legacy > /legacy.txt'
docker -H "unix://$SOCK" create --name old-stopped busybox sh -c 'cat /legacy.txt || true'
docker -H "unix://$SOCK" inspect old-overlay2 --format '{{.Driver}}'
```

预期 `inspect` 输出为 `overlay2`。停止 daemon。

2. 切换到 containerd image store / snapshotter 启动 daemon：

```bash
cat > "$CFG" <<EOF
{
  "features": {
    "containerd-snapshotter": true
  }
}
EOF

dockerd \
  --debug \
  --config-file "$CFG" \
  --host "unix://$SOCK" \
  --pidfile "$PID" \
  --data-root "$ROOT" \
  --exec-root "$EXEC" \
  --iptables=false \
  --ip6tables=false \
  --storage-driver=overlayfs
```

daemon 日志应出现类似信息：

```text
registered legacy storage backend for previously-created containers
```

3. 验证旧容器仍可见、可启动、可删除：

```bash
docker -H "unix://$SOCK" ps -a
docker -H "unix://$SOCK" inspect old-overlay2 --format '{{.Driver}} {{.State.Status}}'
docker -H "unix://$SOCK" start old-stopped
docker -H "unix://$SOCK" stop old-stopped
docker -H "unix://$SOCK" rm old-stopped
```

预期：

- `old-overlay2` / `old-stopped` 不再因为当前 driver 是 `overlayfs` 而从 `ps -a` 消失。
- `inspect` 仍显示旧容器 driver 为 `overlay2`。
- `start/stop/rm` 不应调用当前 `overlayfs` backend 释放旧容器 RW layer。

4. 验证新容器使用当前 default backend：

```bash
docker -H "unix://$SOCK" run --name new-containerd busybox true
docker -H "unix://$SOCK" inspect new-containerd --format '{{.Driver}}'
```

预期新容器 driver 为当前 snapshotter/backend，例如 `overlayfs`。

## 测试矩阵

Phase 1 至少需要覆盖：

- graphdriver daemon 创建 stopped 容器后切换到 containerd image store。
- containerd image store 创建 stopped 容器后切换到另一个 snapshotter。
- 旧容器 `ps`、`inspect`、`start`、`stop`、`restart`、`rm`。
- 新容器创建，确认使用 containerd default backend。
- 新旧容器同时存在时 daemon restart。
- legacy backend 初始化失败。
- `docker rm` legacy 容器不误删 legacy image/lower layer。
- `docker rm` legacy 容器不调用 default backend 的 layer release。
- graphdriver legacy 容器在 containerd default daemon 下 start 时，不设置错误的 snapshotter metadata。
- containerd legacy 容器从 `overlayfs` 切到 `native` 后，仍使用 `overlayfs` 查找 active snapshot。
- backend identity ambiguous 时，daemon 不猜测恢复。

Phase 2 以后再覆盖：

- graphdriver daemon 创建 running 容器，并在 live-restore 场景切换。
- 旧 image 与新 image 同名 tag。
- `docker image ls` 展示 legacy image 并标记 backend。
- `docker image rm` 不误删 legacy image/layer。
- `docker diff`、`docker export`、`docker commit` 的 backend-aware 行为。
- `docker system df` 和 `docker system prune` 的保守行为。

## 结论

containerd 能更自然地支持多个 snapshotter，是因为它把 content store、image metadata、snapshotter 和 container metadata 分离了。image content 按 digest 共享，snapshotter 作为插件按名称访问，container metadata 可以记录自己使用的 snapshotter。

Docker 历史 storage backend 模型则把 daemon 行为强绑定到单一默认 storage driver。即使上游 master 已经引入 containerd image store，Docker daemon 的 restore、image API、prune 和展示语义仍然以单一默认 backend 为中心。

因此，当前提出的方案应聚焦为：

**显式 storage backend 切换后，为历史 backend 容器提供受限的恢复兼容层。**

这不是完整多 image store 共存，而是 storage backend 切换后的历史容器兼容能力。技术上可行，但必须严格限制 legacy backend 的写入面，优先保证旧容器可管理和数据不丢失，再逐步扩展只读镜像视图、导出迁移和安全清理能力。
