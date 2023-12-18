// kad-dht

package dep2p

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/bpfs/dep2p/internal"
	"github.com/bpfs/dep2p/internal/net"
	"github.com/bpfs/dep2p/kbucket/peerdiversity"
	"github.com/bpfs/dep2p/metrics"
	"github.com/bpfs/dep2p/netsize"
	"github.com/bpfs/dep2p/providers"

	"github.com/bpfs/dep2p/rtrefresh"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/sirupsen/logrus"

	"go.opencensus.io/tag"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/multierr"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"

	pb "github.com/bpfs/dep2p/pb"

	kb "github.com/bpfs/dep2p/kbucket"

	record "github.com/libp2p/go-libp2p-record"

	dhtcfg "github.com/bpfs/dep2p/internal/config"

	ds "github.com/ipfs/go-datastore"
	ma "github.com/multiformats/go-multiaddr"
)

var (
	rtFreezeTimeout = 1 * time.Minute // 路由表冻结超时时间
)

const (
	// BaseConnMgrScore 是设置在连接管理器 "kbucket" 标签上的分数基准。
	// 它添加了两个对等体 ID 之间的公共前缀长度。
	baseConnMgrScore = 5
)

type mode int

const (
	modeServer mode = iota + 1 // 服务器模式
	modeClient                 // 客户端模式
)

const (
	kad1 protocol.ID = "/kad/1.0.0" // Kademlia 协议版本 1.0.0
)

const (
	kbucketTag       = "kbucket" // kbucket 标签
	protectedBuckets = 2         // 受保护的桶数量
)

// DeP2PDHT 是经过 S/Kademlia 修改的 Kademlia 实现。
// 它用于实现基本路由模块。
type DeP2PDHT struct {
	host host.Host // 当前主机
	self peer.ID   // 本地节点
	// DeP2PDHT 的 ID 位于 XORKeySpace 中
	// dht.ID 类型表示其内容已从 peer.ID 或 util.Key 进行哈希处理。 这统一了键空间
	selfKey kb.ID // 当前节点的 key, 在 Kademlia DHT 中用于距离计算
	// Peerstore 提供了 Peer 相关信息的线程安全存储。
	peerstore peerstore.Peerstore // 对等节点注册表。用于存储已知的 peer 的信息

	// Datastore 表示任何键值对的存储。
	datastore ds.Datastore // 本地数据存储，用于存储DHT数据

	// RoutingTable 定义路由表。
	routingTable *kb.RoutingTable // DHT 的路由表，存储其他 known peers
	// ProviderStore 表示将节点及其地址与密钥相关联的存储。
	providerStore providers.ProviderStore // 保存内容提供者的信息

	// RtRefreshManager 是一个用于管理 Routing Table 刷新的结构体。
	// 它包含了执行刷新操作所需的上下文、计数器、本地对等节点信息、Routing Table、刷新相关的配置参数和函数、刷新触发请求的通道等。
	rtRefreshManager *rtrefresh.RtRefreshManager // 负责刷新路由表的管理器

	birth time.Time // DHT实例创建的时间

	// Validator 是一个应该由记录验证器实现的接口。
	Validator record.Validator // 记录验证器

	ctx    context.Context    // 用于 DHT 操作的上下文
	cancel context.CancelFunc // 取消 DHT 操作的函数
	wg     sync.WaitGroup     // 等待组，用于等待所有的 goroutines 完成

	// ProtocolMessenger 可用于向对等方发送 DHT 消息并处理他们的响应。
	// 这将有线协议格式与 DHT 协议实现和routing.Routing 接口的实现解耦。
	protoMessenger *pb.ProtocolMessenger          // 协议通信器，用于 DHT 消息传递的信使
	msgSender      pb.MessageSenderWithDisconnect // 用于发送 DHT 消息的组件

	// stripedPutLocks [256]sync.Mutex // 分段锁，用于并发的 Put 操作

	// 我们查询时使用的 DHT 协议。
	// 仅当对等方使用这些协议时，我们才会将其添加到我们的路由表中。
	protocols []protocol.ID // DHT 使用的协议 ID 列表

	// 我们可以响应的 DHT 协议。
	serverProtocols []protocol.ID // DHT 作为服务端使用的协议 ID 列表

	auto   ModeOpt    // 自动模式，例如自动路由表刷新
	mode   mode       // 当前模式
	modeLk sync.Mutex // 模式锁，用于 mode 字段

	bucketSize int // 桶的大小
	alpha      int // 在查找操作中兵法查询的节点数量
	beta       int // 最接近目标且必须响应查询路径才能终止的对等点的数量（查询成功后等待更多响应的阈值）

	// QueryFilterFunc 是在查询时考虑要拨号的对等点时应用的过滤器
	queryPeerFilter QueryFilterFunc // 查询对等节点过滤器函数
	// RouteTableFilterFunc 是在考虑要保留在本地路由表中的连接时应用的过滤器。
	routingTablePeerFilter RouteTableFilterFunc // 路由表对等节点过滤器函数
	// PeerIPGroupFilter 是想要实例化"peerdiversity.Filter"的调用者必须实现的接口。 该接口提供由"peerdiversity.Filter"使用/调用的函数钩子。
	rtPeerDiversityFilter peerdiversity.PeerIPGroupFilter // 路由表对等节点多样性过滤器

	autoRefresh bool // 是否自动刷新路由表

	// LookupCheck 操作超时
	lookupCheckTimeout time.Duration // 查找检查的超时时间
	// 并发进行的 lookupCheck 操作的数量
	lookupCheckCapacity int        // 查找检查的容量
	lookupChecksLk      sync.Mutex // 查找检查的锁

	// 如果修复路由表的所有其他尝试都失败（或者，例如，这是该节点第一次连接到网络），则返回一组引导对等点以进行回退。
	bootstrapPeers func() []peer.AddrInfo // 获取 bootstrap peers 的函数

	maxRecordAge time.Duration // 记录的最大存储时间（有效期）

	// 允许禁用 dht 子系统。 这些应该_仅_在"分叉"DHT 上设置（例如，具有自定义协议和/或专用网络的 DHT）。
	enableProviders, enableValues bool // 是否启用内容提供者和值存储

	disableFixLowPeers bool          // 禁用修复低对等节点
	fixLowPeersChan    chan struct{} // 修复低对等节点的通道

	addPeerToRTChan   chan peer.ID  // 添加对等节点到路由表的通道
	refreshFinishedCh chan struct{} // 刷新完成的通道

	rtFreezeTimeout time.Duration // 路由表冻结超时时间

	// 网络大小估算器
	nsEstimator   *netsize.Estimator // 网络大小估算器
	enableOptProv bool               // 启用可选提供者

	// 用于限制运行中 ADD_PROVIDER RPC 异步性的绑定通道
	optProvJobsPool chan struct{} // 优化的提供者查询的作业池

	// 用于测试的配置变量
	testAddressUpdateProcessing bool // 是否开启地址更新处理的测试

	// addrFilter 用于过滤我们放入对等存储中的地址。
	// 主要用于过滤掉本地主机和本地地址。
	addrFilter func([]ma.Multiaddr) []ma.Multiaddr // 地址过滤函数
}

// New 使用指定的主机和选项创建一个新的 DHT。
// 请注意，连接到 DHT 对等点并不一定意味着它也在 DHT 路由表中。
// 如果路由表具有超过"minRTRefreshThreshold"的对等点，则仅当我们成功从某个对等点获取查询响应或它向我们发送查询时，我们才会将其视为路由表候选者。
func New(ctx context.Context, h host.Host, options ...Option) (*DeP2PDHT, error) {
	// Config 是一个包含构建 DHT 时可以使用的所有选项的结构
	var cfg dhtcfg.Config
	// Apply 将给定选项应用于此选项
	if err := cfg.Apply(append([]Option{dhtcfg.Defaults}, options...)...); err != nil {
		return nil, err
	}

	// ApplyFallbacks 设置在配置创建期间无法应用的默认值，因为它们依赖于其他配置参数（例如 optA 默认为 2x optB）和/或主机
	if err := cfg.ApplyFallbacks(h); err != nil {
		return nil, err
	}

	// Validate 方法用于验证配置项是否符合要求
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// makeDHT 使用给定的主机和配置创建一个新的 DeP2PDHT 对象。
	dht, err := makeDHT(h, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create DHT, err=%s", err)
	}

	dht.autoRefresh = cfg.RoutingTable.AutoRefresh // 设置自动刷新路由表的选项

	dht.maxRecordAge = cfg.MaxRecordAge             // 设置记录的最大存储时间
	dht.enableProviders = cfg.EnableProviders       // 设置是否启用提供者功能
	dht.enableValues = cfg.EnableValues             // 设置是否启用值存储功能
	dht.disableFixLowPeers = cfg.DisableFixLowPeers // 设置是否禁用修复低对等节点功能

	dht.Validator = cfg.Validator                                    // 设置验证器
	dht.msgSender = net.NewMessageSenderImpl(h, dht.protocols)       // 创建消息发送器
	dht.protoMessenger, err = pb.NewProtocolMessenger(dht.msgSender) // 创建协议信使
	if err != nil {
		return nil, err
	}

	dht.testAddressUpdateProcessing = cfg.TestAddressUpdateProcessing // 设置测试地址更新处理的选项

	dht.auto = cfg.Mode // 描述 DHT 应该运行的模式的选项
	switch cfg.Mode {
	case ModeAuto, ModeClient:
		// 设置 DHT 的模式为客户端模式
		dht.mode = modeClient
	case ModeAutoServer, ModeServer:
		// 设置 DHT 的模式为服务器模式
		dht.mode = modeServer
	default:
		// 如果模式无效，返回错误
		return nil, fmt.Errorf("invalid dht mode %d", cfg.Mode)
	}

	if dht.mode == modeServer {
		// moveToServerMode 通过 libp2p identify 更新来广告我们能够响应 DHT 查询，并设置适当的流处理程序。
		// 注意：为了与旧版本的 DHT 协议进行互操作性，我们可能支持使用除主要协议之外的协议来响应查询。
		if err := dht.moveToServerMode(); err != nil {
			return nil, err
		}
	}

	// 注册事件总线和网络通知
	if err := dht.startNetworkSubscriber(); err != nil {
		return nil, err
	}

	// go-routine 来确保我们在对等存储中始终拥有 RT 对等地址，因为 RT 成员资格与连接性分离
	go dht.persistRTPeersInPeerStore()

	// rtPeerLoop 用于管理 DHT（分布式哈希表）中路由表（Routing Table）的状态。
	dht.rtPeerLoop()

	// 使用当前连接的 DHT 服务器对等点填充路由表
	for _, p := range dht.host.Network().Peers() {
		// peerFound 验证找到的对等点是否通告 DHT 协议并探测它以确保它按预期响应 DHT 查询。
		// 如果它无法应答，则不会将其添加到路由表中。
		dht.peerFound(p)
	}

	// Start 启动 RtRefreshManager 实例的循环处理函数。
	dht.rtRefreshManager.Start()

	// 监听修复低对等点并尝试修复路由表
	if !dht.disableFixLowPeers {
		// runFixLowPeersLoop 管理修复LowPeers 的并发请求
		dht.runFixLowPeersLoop()
	}

	return dht, nil
}

// PeerID 返回 DHT 节点的 Peer ID。
func (dht *DeP2PDHT) PeerID() peer.ID {
	return dht.self
}

// PeerKey 返回一个从 DHT 节点的 Peer ID 转换而来的 DHT 键。
func (dht *DeP2PDHT) PeerKey() []byte {
	return kb.ConvertPeerID(dht.self)
}

// Host returns the libp2p host this DHT is operating with.
func (dht *DeP2PDHT) Host() host.Host {
	return dht.host
}

// Context 返回 DHT 的上下文。
func (dht *DeP2PDHT) Context() context.Context {
	return dht.ctx
}

// RoutingTable 返回 DHT 的路由表。
func (dht *DeP2PDHT) RoutingTable() *kb.RoutingTable {
	return dht.routingTable
}

// Mode allows introspection of the operation mode of the DHT
func (dht *DeP2PDHT) Mode() ModeOpt {
	return dht.auto
}

// Close 调用 Process Close。
func (dht *DeP2PDHT) Close() error {
	// 取消上下文
	dht.cancel()
	dht.wg.Wait()

	var wg sync.WaitGroup
	closes := [...]func() error{
		dht.rtRefreshManager.Close,
		dht.providerStore.Close,
	}
	var errors [len(closes)]error
	wg.Add(len(errors))
	for i, c := range closes {
		go func(i int, c func() error) {
			defer wg.Done()
			errors[i] = c()
		}(i, c)
	}
	wg.Wait()

	return multierr.Combine(errors[:]...)
}

// Ping 向传入的对等点发送 ping 消息并等待响应。
func (dht *DeP2PDHT) Ping(ctx context.Context, p peer.ID) error {
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.Ping", trace.WithAttributes(attribute.Stringer("PeerID", p)))
	defer span.End()
	return dht.protoMessenger.Ping(ctx, p)
}

// makeDHT 使用给定的主机和配置创建一个新的 DeP2PDHT 对象。
func makeDHT(h host.Host, cfg dhtcfg.Config) (*DeP2PDHT, error) {
	var protocols, serverProtocols []protocol.ID

	v1proto := cfg.ProtocolPrefix + kad1

	if cfg.V1ProtocolOverride != "" {
		v1proto = cfg.V1ProtocolOverride
	}

	protocols = []protocol.ID{v1proto}
	serverProtocols = []protocol.ID{v1proto}

	dht := &DeP2PDHT{
		datastore: cfg.Datastore, // 数据存储
		self:      h.ID(),        // 本地对等节点的标识符

		// ConvertPeerID 通过散列对等 ID（多重散列）来创建 DHT ID
		selfKey: kb.ConvertPeerID(h.ID()), // 将本地对等节点的标识符转换为键

		// Peerstore 返回主机的对等地址和密钥存储库。
		peerstore: h.Peerstore(), // 对等节点存储

		host:                   h,                                // 主机
		birth:                  time.Now(),                       // DHT 对象的创建时间
		protocols:              protocols,                        // 支持的协议
		serverProtocols:        serverProtocols,                  // 服务器支持的协议
		bucketSize:             cfg.BucketSize,                   // 路由表桶的大小
		alpha:                  cfg.Concurrency,                  // 并发查询的数量
		beta:                   cfg.Resiliency,                   // 并发查询失败时的重试次数
		lookupCheckCapacity:    cfg.LookupCheckConcurrency,       // 查询检查的并发度
		queryPeerFilter:        cfg.QueryPeerFilter,              // 查询对等节点的过滤器
		routingTablePeerFilter: cfg.RoutingTable.PeerFilter,      // 路由表对等节点的过滤器
		rtPeerDiversityFilter:  cfg.RoutingTable.DiversityFilter, // 路由表对等节点的多样性过滤器
		addrFilter:             cfg.AddressFilter,                // 地址过滤器

		fixLowPeersChan: make(chan struct{}, 1), // 修复低对等节点的通道

		addPeerToRTChan:   make(chan peer.ID),  // 添加对等节点到路由表的通道
		refreshFinishedCh: make(chan struct{}), // 刷新完成的通知通道

		enableOptProv:   cfg.EnableOptimisticProvide, // 是否启用乐观提供功能
		optProvJobsPool: nil,                         // 乐观提供作业池
	}

	// 最大上次成功出站阈值
	var maxLastSuccessfulOutboundThreshold time.Duration

	// 该阈值是根据我们在刷新周期中查询对等方之前应经过的预期时间量来计算的。
	// 要理解产生这些精确方程的数学魔法，请耐心等待，解释它的文档很快就会发布。
	if cfg.Concurrency < cfg.BucketSize { // (alpha < K) // 如果并发查询的数量小于路由表桶的大小
		l1 := math.Log(float64(1) / float64(cfg.BucketSize))                              // (Log(1/K))
		l2 := math.Log(float64(1) - (float64(cfg.Concurrency) / float64(cfg.BucketSize))) // Log(1 - (alpha / K))
		maxLastSuccessfulOutboundThreshold = time.Duration(l1 / l2 * float64(cfg.RoutingTable.RefreshInterval))
	} else {
		maxLastSuccessfulOutboundThreshold = cfg.RoutingTable.RefreshInterval
	}

	// 使用理论有用阈值的两倍构建路由表，以使较旧的对等点保持更长时间
	rt, err := makeRoutingTable(dht, cfg, 2*maxLastSuccessfulOutboundThreshold)
	if err != nil {
		// 构建路由表失败，返回错误
		return nil, fmt.Errorf("failed to construct routing table,err=%s", err)
	}
	dht.routingTable = rt                   // 定义路由表
	dht.bootstrapPeers = cfg.BootstrapPeers // 引导对等节点列表

	dht.lookupCheckTimeout = cfg.RoutingTable.RefreshQueryTimeout // 查询超时时间

	// 初始化网络大小估计器
	dht.nsEstimator = netsize.NewEstimator(h.ID(), rt, cfg.BucketSize)

	// 启用可选提供者
	if dht.enableOptProv {
		dht.optProvJobsPool = make(chan struct{}, cfg.OptimisticProvideJobsPoolSize)
	}

	// 路由表刷新管理器
	dht.rtRefreshManager, err = makeRtRefreshManager(dht, cfg, maxLastSuccessfulOutboundThreshold)
	if err != nil {
		// 构建刷新管理器失败，返回错误
		return nil, fmt.Errorf("failed to construct RT Refresh Manager,err=%s", err)
	}

	// 创建从原始上下文派生的标记上下文
	// DHT上下文应该在进程关闭时完成
	dht.ctx, dht.cancel = context.WithCancel(dht.newContextWithLocalTags(context.Background()))

	if cfg.ProviderStore != nil {
		// 提供者信息的存储接口
		dht.providerStore = cfg.ProviderStore
	} else {
		dht.providerStore, err = providers.NewProviderManager(h.ID(), dht.peerstore, cfg.Datastore)
		if err != nil {
			// 初始化默认提供者管理器失败，返回错误
			return nil, fmt.Errorf("initializing default provider manager (%v)", err)
		}
	}

	// 路由表冻结超时时间
	dht.rtFreezeTimeout = rtFreezeTimeout

	return dht, nil
}

// moveToServerMode 通过 libp2p identify 更新来广告我们能够响应 DHT 查询，并设置适当的流处理程序。
// 注意：为了与旧版本的 DHT 协议进行互操作性，我们可能支持使用除主要协议之外的协议来响应查询。
func (dht *DeP2PDHT) moveToServerMode() error {
	// 将模式设置为服务器模式
	dht.mode = modeServer
	for _, p := range dht.serverProtocols {
		// 设置流处理程序，用于处理新的流
		dht.host.SetStreamHandler(p, dht.handleNewStream)
	}
	return nil
}

// TODO 这是一个笨拙、可怕的解决方案，程序员需要被称为仓鼠的母亲。
// 一旦 https://github.com/libp2p/go-libp2p/issues/800 进入，就应该删除。
func (dht *DeP2PDHT) persistRTPeersInPeerStore() {
	tickr := time.NewTicker(peerstore.RecentlyConnectedAddrTTL / 3) // 创建定时器
	defer tickr.Stop()                                              // 停止定时器

	for {
		select {
		// 定时器触发
		case <-tickr.C:
			// 获取路由表中的对等节点列表
			ps := dht.routingTable.ListPeers()
			for _, p := range ps {
				// 更新对等节点的地址
				dht.peerstore.UpdateAddrs(p, peerstore.RecentlyConnectedAddrTTL, peerstore.RecentlyConnectedAddrTTL)
			}
			// 上下文被取消
		case <-dht.ctx.Done():
			return
		}
	}
}

// rtPeerLoop 用于管理 DHT（分布式哈希表）中路由表（Routing Table）的状态。
// 是一个核心的事件循环，负责管理路由表的状态和行为。
// 这包括何时添加新的对等节点，何时标记对等节点为不可替换，以及如何处理引导和路由表的刷新。
func (dht *DeP2PDHT) rtPeerLoop() {
	dht.wg.Add(1)
	go func() {
		defer dht.wg.Done()

		var bootstrapCount uint      // 引导计数
		var isBootsrapping bool      // 是否正在引导
		var timerCh <-chan time.Time // 定时器通道，用于出发一些定时任务

		for {
			select {
			// 如果定时器触发，将路由表中的所有对等节点标记为不可替换
			case <-timerCh:
				// MarkAllPeersIrreplaceable 将路由表中的所有对等点标记为不可替换
				// 这意味着我们永远不会替换表中现有的对等点来为新的对等点腾出空间。
				// 但是，仍然可以通过调用 `RemovePeer` API 来删除它们。
				dht.routingTable.MarkAllPeersIrreplaceable()

			// 添加新对等节点到路由表的逻辑
			// - 如果路由表为空，则进入引导模式。
			// - 尝试将新的对等节点添加到路由表。
			// - 如果成功添加，则可能需要修复路由表。
			// - 如果对等节点已存在，更新其最后成功的出站查询时间。
			case p := <-dht.addPeerToRTChan:
				if dht.routingTable.Size() == 0 {
					isBootsrapping = true
					bootstrapCount = 0
					timerCh = nil
				}
				// queryPeer 设置为 true，因为我们只尝试将查询的对等点添加到 RT
				//
				// 如果对等点新添加到路由表中，TryAddPeer 返回一个布尔值，设置为 true，否则返回 false。
				// 它还返回将对等方添加到路由表时发生的任何错误。
				// 如果错误不为零，则布尔值将始终为 false，即，如果对等点尚不存在，则不会将其添加到路由表中。
				// 返回值 false 且 error=nil 表示对等点已存在于路由表中。
				newlyAdded, err := dht.routingTable.TryAddPeer(p, int(dht.mode), true, isBootsrapping)
				if err != nil {
					// 对等节点未添加。
					continue
				}
				if newlyAdded {
					// 对等节点已添加到路由表中，如果需要的话现在可以修复它。
					dht.fixRTIfNeeded()
				} else {
					// 对等点已经在我们的 RT 中，但我们刚刚成功查询了它，因此让我们在查询时间上增加一点，这样我们就不会太快对其进行 ping 来进行活动检查。
					dht.routingTable.UpdateLastSuccessfulOutboundQueryAt(p, time.Now())
				}

			// 当路由表刷新完成时触发
			// - 引导计数增加。
			// - 如果达到两次引导，则启动一个新的定时器。
			case <-dht.refreshFinishedCh:
				bootstrapCount = bootstrapCount + 1
				if bootstrapCount == 2 {
					timerCh = time.NewTimer(dht.rtFreezeTimeout).C
				}

				old := isBootsrapping
				isBootsrapping = false
				if old {
					// RefreshNoWait 请求刷新管理器刷新路由表。 但是，如果请求无法通过，它会继续前进而不会阻塞。
					dht.rtRefreshManager.RefreshNoWait()
				}

			// 如果 DHT 上下文结束，返回并结束 Goroutine。
			case <-dht.ctx.Done():
				return
			}
		}
	}()
}

// peerFound 验证找到的对等点是否通告 DHT 协议并探测它以确保它按预期响应 DHT 查询。
// 如果它无法应答，则不会将其添加到路由表中。
func (dht *DeP2PDHT) peerFound(p peer.ID) {
	// 如果对等点已在路由表中或相应的存储桶已满，则不要尝试添加新的对等点 ID
	if !dht.routingTable.UsefulNewPeer(p) {
		return
	}

	// 验证远程对等点是否通告正确的 dht 协议
	b, err := dht.validRTPeer(p)
	if err != nil {
		logrus.Errorln("failed to validate if peer is a DHT peer", "peer", p, "error", err)
	} else if b {

		// 检查是否达到最大并发查找检查数
		dht.lookupChecksLk.Lock()
		if dht.lookupCheckCapacity == 0 {
			dht.lookupChecksLk.Unlock()
			// 如果达到了最大并发查找检查数，则删除新的peer.ID
			return
		}
		dht.lookupCheckCapacity--
		dht.lookupChecksLk.Unlock()

		go func() {
			livelinessCtx, cancel := context.WithTimeout(dht.ctx, dht.lookupCheckTimeout)
			defer cancel()

			// 执行 FIND_NODE 查询

			// lookupCheck 向远程peer.ID执行查找请求，验证它是否能够正确应答
			err := dht.lookupCheck(livelinessCtx, p)

			dht.lookupChecksLk.Lock()
			dht.lookupCheckCapacity++
			dht.lookupChecksLk.Unlock()

			if err != nil {
				logrus.Debug("connected peer not answering DHT request as expected", "peer", p, "error", err)
				return
			}

			// 如果FIND_NODE成功，则认为该对等点有效

			// validPeerFound 向路由表发出信号，表明我们已找到支持 DHT 协议的对等点，并且刚刚正确回答了 DHT FindPeers
			dht.validPeerFound(p)
		}()
	}
}

// runFixLowPeersLoop 管理修复LowPeers 的并发请求
func (dht *DeP2PDHT) runFixLowPeersLoop() {
	// 增加等待组计数器
	dht.wg.Add(1)
	go func() {
		// 减少等待组计数器
		defer dht.wg.Done()

		// 调用 fixLowPeers 方法
		dht.fixLowPeers()

		ticker := time.NewTicker(periodicBootstrapInterval) // 创建定时器
		defer ticker.Stop()                                 // 停止定时器

		for {
			select {
			// 监听 fixLowPeersChan 通道
			case <-dht.fixLowPeersChan:
				// 定时器触发
			case <-ticker.C:
				// 上下文被取消
			case <-dht.ctx.Done():
				return
			}

			// 再次调用 fixLowPeers 方法
			dht.fixLowPeers()
		}
	}()
}

// makeRoutingTable 构建 RoutingTable 对象
func makeRoutingTable(dht *DeP2PDHT, cfg dhtcfg.Config, maxLastSuccessfulOutboundThreshold time.Duration) (*kb.RoutingTable, error) {
	// 制作路由表分集过滤器
	var filter *peerdiversity.Filter
	if dht.rtPeerDiversityFilter != nil {
		df, err := peerdiversity.NewFilter(dht.rtPeerDiversityFilter, "rt/diversity", func(p peer.ID) int {
			return kb.CommonPrefixLen(dht.selfKey, kb.ConvertPeerID(p))
		})

		if err != nil {
			return nil, fmt.Errorf("failed to construct peer diversity filter: %w", err)
		}

		filter = df
	}

	// NewRoutingTable 使用给定的存储桶大小、本地 ID 和延迟容忍度创建新的路由表。
	rt, err := kb.NewRoutingTable(cfg.BucketSize, dht.selfKey, time.Minute, dht.host.Peerstore(), maxLastSuccessfulOutboundThreshold, filter)
	if err != nil {
		return nil, err
	}

	cmgr := dht.host.ConnManager()

	// 当有新的对等节点加入时的回调函数
	rt.PeerAdded = func(p peer.ID) {
		commonPrefixLen := kb.CommonPrefixLen(dht.selfKey, kb.ConvertPeerID(p))
		if commonPrefixLen < protectedBuckets {
			// 将对等节点标记为受保护状态
			cmgr.Protect(p, kbucketTag)
		} else {
			// 标记对等节点
			cmgr.TagPeer(p, kbucketTag, baseConnMgrScore)
		}
	}
	// 当有对等节点移除时的回调函数
	rt.PeerRemoved = func(p peer.ID) {
		cmgr.Unprotect(p, kbucketTag) // 取消对等节点的保护状态
		cmgr.UntagPeer(p, kbucketTag) // 取消对等节点的标记

		// 尝试修复 Routing Table
		dht.fixRTIfNeeded()
	}

	return rt, err
}

// makeRtRefreshManager 构建 RtRefreshManager 对象
func makeRtRefreshManager(dht *DeP2PDHT, cfg dhtcfg.Config, maxLastSuccessfulOutboundThreshold time.Duration) (*rtrefresh.RtRefreshManager, error) {
	keyGenFnc := func(cpl uint) (string, error) {
		p, err := dht.routingTable.GenRandPeerID(cpl)
		return string(p), err
	}

	queryFnc := func(ctx context.Context, key string) error {
		_, err := dht.GetClosestPeers(ctx, key)
		return err
	}

	r, err := rtrefresh.NewRtRefreshManager(
		dht.host, dht.routingTable, cfg.RoutingTable.AutoRefresh, // 使用主机、路由表和自动刷新选项构建 RtRefreshManager
		keyGenFnc,                            // 用于生成查询键的函数
		queryFnc,                             // 用于执行查询的函数
		dht.lookupCheck,                      // 用于执行查找检查的函数
		cfg.RoutingTable.RefreshQueryTimeout, // 刷新查询的超时时间
		cfg.RoutingTable.RefreshInterval,     // 刷新间隔
		maxLastSuccessfulOutboundThreshold,   // 最大上次成功的出站请求时间阈值
		dht.refreshFinishedCh)                // 刷新完成的通知通道

	return r, err
}

// newContextWithLocalTags 返回一个新的 context.Context，其中填充了 InstanceID 和 PeerID 键。
// 它还会将需要添加到上下文的任何额外标签作为 tag.Mutators。
func (dht *DeP2PDHT) newContextWithLocalTags(ctx context.Context, extraTags ...tag.Mutator) context.Context {
	extraTags = append(
		extraTags,
		tag.Upsert(metrics.KeyPeerID, dht.self.String()),
		tag.Upsert(metrics.KeyInstanceID, fmt.Sprintf("%p", dht)),
	)
	ctx, _ = tag.New(
		ctx,
		extraTags...,
	) // 忽略错误，因为它与此代码的实际功能无关。
	return ctx
}

// fixRTIfNeeded 检查路由表 (RT) 是否需要修复，例如通过向其中添加更多的对等节点。
// DHT 维护了一个对等节点的路由表以实现高效的查找，此函数确保该表已充分填充。
func (dht *DeP2PDHT) fixRTIfNeeded() {
	// 尝试向 fixLowPeersChan 发送一个信号（一个空结构体）。
	// 如果通道已满或没有监听器准备接收该信号，此操作不会阻塞并会进入 default 分支。
	select {
	case dht.fixLowPeersChan <- struct{}{}:
		// 已发送信号，表示路由表可能需要修复。
	default:
		// 通道正忙或已满。在这种情况下，我们什么都不做，直接返回。
	}
}

// lookupCheck 向远程peer.ID执行查找请求，验证它是否能够正确应答
func (dht *DeP2PDHT) lookupCheck(ctx context.Context, p peer.ID) error {
	// ////////////////////////////////////////////////////////////////////
	var val pb.Message_ClosestPeers
	val.Self = []byte(dht.self) // 当前节点的 pper ID
	val.Mode = int32(dht.mode)  // 当前运行模式
	valByte, err := val.Marshal()
	if err != nil {
		return err
	}
	//////////////////////////////////////////////////////////////////////

	// 向p 请求其自己的peer.ID 的查找请求
	peerids, err := dht.protoMessenger.GetClosestPeers(ctx, p, p, valByte)
	// p 应该至少返回它自己的peerid
	if err == nil && len(peerids) == 0 {
		return fmt.Errorf("peer %s failed to return its closest peers, got %d", p, len(peerids))
	}
	return err
}

// validPeerFound 向路由表发出信号，表明我们已找到支持 DHT 协议的对等点，并且刚刚正确回答了 DHT FindPeers
func (dht *DeP2PDHT) validPeerFound(p peer.ID) {

	select {
	// 将找到的对等节点添加到路由表的通道中
	case dht.addPeerToRTChan <- p:
	case <-dht.ctx.Done():
		return
	}
}

// fixLowPeers 如果低于阈值，尝试将更多对等节点添加到路由表中
func (dht *DeP2PDHT) fixLowPeers() {
	if dht.routingTable.Size() > minRTRefreshThreshold {
		return
	}

	// 尝试将所有已连接的对等节点添加到路由表中（如果它们尚未存在）
	for _, p := range dht.host.Network().Peers() {
		dht.peerFound(p)
	}

	// TODO 主动引导
	// 在连接到引导节点之前，我们应该首先使用先前快照中已知的非引导节点
	// 尝试将它们添加到路由表中。参考：https://github.com/libp2p/go-libp2p-kad-dht/issues/387。
	if dht.routingTable.Size() == 0 && dht.bootstrapPeers != nil {
		bootstrapPeers := dht.bootstrapPeers()
		if len(bootstrapPeers) == 0 {
			// 没有对等节点，继续没有意义！
			return
		}

		found := 0
		for _, i := range rand.Perm(len(bootstrapPeers)) {
			ai := bootstrapPeers[i]
			err := dht.Host().Connect(dht.ctx, ai)
			if err == nil {
				found++
			} else {
				logrus.Warnln("引导失败", "对等节点", ai.ID, "错误", err)
			}

			// 等待两个引导节点，或者尝试所有引导节点。
			//
			// 为什么是两个？理论上，通常一个引导节点就足够了。
			// 但是，如果网络重新启动，每个人都只连接一个引导节点，
			// 我们将得到一个大部分分区的网络。
			//
			// 因此，我们总是使用两个随机引导节点进行引导。
			if found == maxNBoostrappers {
				break
			}
		}
	}

	// 如果路由表中仍然没有对等节点（可能是因为 Identify 操作尚未完成），
	// 则没有必要触发 Refresh 操作。
	if dht.routingTable.Size() == 0 {
		return
	}

	if dht.autoRefresh {
		dht.rtRefreshManager.RefreshNoWait()
	}
}

// getMode 用于获取当前的模式。
func (dht *DeP2PDHT) getMode() mode {
	dht.modeLk.Lock()
	defer dht.modeLk.Unlock()
	return dht.mode
}

// betterPeersToQuery 返回 nearestPeersToQuery 的结果，并进行了一些额外的过滤。
func (dht *DeP2PDHT) betterPeersToQuery(pmes *pb.Message, from peer.ID, count int) []peer.ID {
	// 调用 nearestPeersToQuery 获取最近的可查询对等节点
	closer := dht.nearestPeersToQuery(pmes, count)

	// 如果没有可用的节点，则返回 nil
	if closer == nil {
		logrus.Infoln("no closer peers to send", from)
		return nil
	}

	filtered := make([]peer.ID, 0, len(closer))
	for _, clp := range closer {

		// 如果对等节点与当前节点相同，则出现错误，不应该返回自身
		if clp == dht.self {
			logrus.Errorln("BUG betterPeersToQuery: attempted to return self! this shouldn't happen...")
			return nil
		}
		// 不要将对等节点返回给自己
		if clp == from {
			continue
		}

		filtered = append(filtered, clp)
	}

	// 返回经过过滤的更近的对等节点
	// 好的，看起来节点更近
	return filtered
}

// nearestPeersToQuery 返回最接近的对等点的路由表。
func (dht *DeP2PDHT) nearestPeersToQuery(pmes *pb.Message, count int) []peer.ID {
	closer := dht.routingTable.NearestPeers(kb.ConvertKey(string(pmes.GetKey())), count)
	return closer
}

// peerStoppedDHT 向路由表发出信号，表明对等方无法再响应 DHT 查询。
func (dht *DeP2PDHT) peerStoppedDHT(p peer.ID) {
	logrus.Debug("peer stopped dht", "peer", p)
	// 不支持 DHT 协议的对等点对我们来说是死的。
	// 在它再次开始支持 DHT 协议之前，再进行对话是没有意义的。
	dht.routingTable.RemovePeer(p)
}

// setMode 用于设置 DHT 的模式。
func (dht *DeP2PDHT) setMode(m mode) error {
	dht.modeLk.Lock()
	defer dht.modeLk.Unlock()

	// 如果传入的模式与当前模式相同，则直接返回
	if m == dht.mode {
		return nil
	}

	switch m {
	case modeServer:
		// 切换到服务器模式
		return dht.moveToServerMode()
	case modeClient:
		// 切换到客户端模式
		return dht.moveToClientMode()
	default:
		// 无法识别的 DHT 模式，返回错误
		return fmt.Errorf("unrecognized dht mode: %d", m)
	}
}

// moveToClientMode 停止广告（并通过 libp2p 识别更新取消广告）我们能够响应 DHT 查询并删除适当的流处理程序。
// 我们还杀死所有使用已处理协议的入站流。
// 注意：我们可能支持使用主要协议之外的协议响应查询，以支持与旧版本 DHT 协议的互操作性。
func (dht *DeP2PDHT) moveToClientMode() error {
	// 将模式设置为客户端模式
	dht.mode = modeClient
	for _, p := range dht.serverProtocols {
		// 移除流处理程序
		dht.host.RemoveStreamHandler(p)
	}

	// 创建协议集合，用于存储已处理的协议
	pset := make(map[protocol.ID]bool)
	for _, p := range dht.serverProtocols {
		pset[p] = true
	}

	// 关闭所有使用已处理协议的入站流
	for _, c := range dht.host.Network().Conns() {
		for _, s := range c.GetStreams() {
			if pset[s.Protocol()] {
				if s.Stat().Direction == network.DirInbound {
					_ = s.Reset()
				}
			}
		}
	}
	return nil
}

func (dht *DeP2PDHT) maybeAddAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	// 不要为自己或我们连接的同伴添加地址。 我们有更好的。
	if p == dht.self || dht.host.Network().Connectedness(p) == network.Connected {
		return
	}
	if dht.addrFilter != nil {
		addrs = dht.addrFilter(addrs)
	}
	dht.peerstore.AddAddrs(p, addrs, ttl)
}
