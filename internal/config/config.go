package config

import (
	"fmt"
	"time"

	"github.com/bpfs/dep2p/providers"

	"github.com/bpfs/dep2p/kbucket/peerdiversity"

	"github.com/ipfs/boxo/ipns"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	record "github.com/libp2p/go-libp2p-record"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// DefaultPrefix 是默认附加到所有 DHT 协议的应用程序特定前缀。
const DefaultPrefix protocol.ID = "/ipfs"

const defaultBucketSize = 20

// ModeOpt 描述 dht 应以何种模式运行
type ModeOpt int

// QueryFilterFunc 是在查询时考虑要拨打的对等方时应用的过滤器
type QueryFilterFunc func(dht interface{}, ai peer.AddrInfo) bool

// RouteTableFilterFunc 是在考虑要保留在本地路由表中的连接时应用的过滤器。
type RouteTableFilterFunc func(dht interface{}, p peer.ID) bool

// Config 是一个包含构建 DHT 时可以使用的所有选项的结构。
type Config struct {
	Datastore              ds.Batching             // 用于存储 DHT 数据的数据存储接口
	Validator              record.Validator        // 用于验证记录的验证器接口
	ValidatorChanged       bool                    // 如果 true 意味着验证器已更改并且不应使用默认值
	Mode                   ModeOpt                 // 描述 DHT 应该运行的模式的选项
	ProtocolPrefix         protocol.ID             // 默认情况下附加到所有 DHT 协议的应用程序特定前缀
	V1ProtocolOverride     protocol.ID             // 用于覆盖 V1 协议的协议前缀
	BucketSize             int                     // 路由表中每个桶的最大对等方数量
	Concurrency            int                     // 在执行 DHT 查询时允许的最大并发请求数量
	Resiliency             int                     // 在查询期间允许的最大重试次数
	MaxRecordAge           time.Duration           // 记录在 DHT 中存储的最长时间
	EnableProviders        bool                    // 指示是否启用 DHT 的提供者功能
	EnableValues           bool                    // 指示是否启用 DHT 的值存储功能
	ProviderStore          providers.ProviderStore // 提供者信息的存储接口
	QueryPeerFilter        QueryFilterFunc         // 在查询时考虑要拨号的对等方时应用的过滤器
	LookupCheckConcurrency int                     // 在执行查找操作时允许的最大并发请求数量

	RoutingTable struct {
		RefreshQueryTimeout time.Duration                   // 刷新路由表的查询超时时间
		RefreshInterval     time.Duration                   // 刷新路由表的时间间隔
		AutoRefresh         bool                            // 指示是否自动刷新路由表
		LatencyTolerance    time.Duration                   // 在考虑对等方时允许的最大延迟
		CheckInterval       time.Duration                   // 检查路由表中对等方的时间间隔
		PeerFilter          RouteTableFilterFunc            // 在考虑保留在本地路由表中的连接时应用的过滤器
		DiversityFilter     peerdiversity.PeerIPGroupFilter // 用于对对等方进行分组的过滤器
	}

	BootstrapPeers func() []peer.AddrInfo              // 用于引导 DHT 的对等方地址信息的函数
	AddressFilter  func([]ma.Multiaddr) []ma.Multiaddr // 用于过滤对等方地址的函数

	// 测试特定的配置选项
	DisableFixLowPeers          bool // 指示是否禁用修复低对等方的功能
	TestAddressUpdateProcessing bool // 指示是否启用地址更新处理的测试功能

	EnableOptimisticProvide       bool // 指示是否启用乐观提供功能
	OptimisticProvideJobsPoolSize int  // 是乐观提供作业池的大小
}

// EmptyQueryFilter 是一个空的查询对等方过滤器函数，始终返回 true。
func EmptyQueryFilter(_ interface{}, ai peer.AddrInfo) bool { return true }

// EmptyRTFilter 是一个空的路由表过滤器函数，始终返回 true。
func EmptyRTFilter(_ interface{}, p peer.ID) bool { return true }

// Apply 将给定选项应用于此选项
func (c *Config) Apply(opts ...Option) error {
	for i, opt := range opts {
		// 调用选项函数，并将 Config 结构体作为参数传递给它
		if err := opt(c); err != nil {
			return fmt.Errorf("dht option %d failed: %s", i, err)
		}
	}
	return nil
}

// ApplyFallbacks 设置在配置创建期间无法应用的默认值，因为它们依赖于其他配置参数（例如 optA 默认为 2x optB）和/或主机
func (c *Config) ApplyFallbacks(h host.Host) error {
	// 如果验证器未被修改
	if !c.ValidatorChanged {
		nsval, ok := c.Validator.(record.NamespacedValidator)
		// 如果验证器是 NamespacedValidator 类型
		if ok {
			// 如果验证器中不存在 "pk" 命名空间
			if _, pkFound := nsval["pk"]; !pkFound {
				// 在验证器中添加 "pk" 命名空间，并使用 PublicKeyValidator 作为其值
				nsval["pk"] = record.PublicKeyValidator{}
			}
			// 如果验证器中不存在 "ipns" 命名空间
			if _, ipnsFound := nsval["ipns"]; !ipnsFound {
				// 在验证器中添加 "ipns" 命名空间，并使用带有 Peerstore 的 ipns.Validator 作为其值
				nsval["ipns"] = ipns.Validator{KeyBook: h.Peerstore()}
			}

			// 如果验证器不是 NamespacedValidator 类型，则返回错误，表示默认验证器已被修改但未标记为已更改
		} else {
			return fmt.Errorf("the default Validator was changed without being marked as changed")
		}
	}
	return nil
}

// Option 是 DHT 选项类型。
type Option func(*Config) error

// Defaults 是默认的 DHT 选项。 此选项将自动添加到您传递给 DHT 构造函数的任何选项之前。
var Defaults = func(o *Config) error {
	o.Validator = record.NamespacedValidator{}           // 设置验证器为 NamespacedValidator 类型的默认值
	o.Datastore = dssync.MutexWrap(ds.NewMapDatastore()) // 设置数据存储为带有互斥锁的 MapDatastore
	o.ProtocolPrefix = DefaultPrefix                     // 设置协议前缀为默认值
	o.EnableProviders = true                             // 启用提供者功能
	o.EnableValues = true                                // 启用值功能
	o.QueryPeerFilter = EmptyQueryFilter                 // 设置查询对等筛选器为空筛选器

	o.RoutingTable.LatencyTolerance = 10 * time.Second    // 设置路由表的延迟容忍度为 10 秒
	o.RoutingTable.RefreshQueryTimeout = 10 * time.Second // 设置路由表刷新查询超时时间为 10 秒
	o.RoutingTable.RefreshInterval = 10 * time.Minute     // 设置路由表刷新间隔为 10 分钟
	o.RoutingTable.AutoRefresh = true                     // 启用路由表的自动刷新
	o.RoutingTable.PeerFilter = EmptyRTFilter             // 设置路由表的对等筛选器为空筛选器

	o.MaxRecordAge = providers.ProvideValidity // 设置记录的最大有效期为提供者的有效期

	o.BucketSize = defaultBucketSize // 设置桶的大小为默认值
	o.Concurrency = 10               // 设置并发数为 10
	o.Resiliency = 3                 // 设置容错性为 3
	o.LookupCheckConcurrency = 256   // 设置查找检查并发数为 256

	// MAGIC：将其设置为 OptProvReturnRatio * BucketSize 的倍数是有意义的。 我们选择了4的倍数。
	o.OptimisticProvideJobsPoolSize = 60 // 设置乐观提供作业池的大小为 60

	return nil
}

// Validate 方法用于验证配置项是否符合要求。
func (c *Config) Validate() error {
	// 如果协议前缀不是默认值，则返回 nil
	if c.ProtocolPrefix != DefaultPrefix {
		return nil
	}
	// 如果桶的大小不是默认值，则返回错误信息
	if c.BucketSize != defaultBucketSize {
		return fmt.Errorf("protocol prefix %s must use bucket size %d", DefaultPrefix, defaultBucketSize)
	}
	// 如果未启用提供者功能，则返回错误信息
	if !c.EnableProviders {
		return fmt.Errorf("protocol prefix %s must have providers enabled", DefaultPrefix)
	}
	// 如果未启用值功能，则返回错误信息
	if !c.EnableValues {
		return fmt.Errorf("protocol prefix %s must have values enabled", DefaultPrefix)
	}

	// 将验证器转换为 NamespacedValidator 类型，并检查是否成功转换
	nsval, isNSVal := c.Validator.(record.NamespacedValidator)
	// 如果验证器不是 NamespacedValidator 类型，则返回错误信息
	if !isNSVal {
		return fmt.Errorf("protocol prefix %s must use a namespaced Validator", DefaultPrefix)
	}

	// 如果命名空间验证器的数量不等于 2，则返回错误信息
	if len(nsval) != 2 {
		return fmt.Errorf("protocol prefix %s must have exactly two namespaced validators - /pk and /ipns", DefaultPrefix)
	}

	// 如果不存在 /pk 命名空间验证器或者该验证器不是 record.PublicKeyValidator 类型，则返回错误信息
	if pkVal, pkValFound := nsval["pk"]; !pkValFound {
		return fmt.Errorf("protocol prefix %s must support the /pk namespaced Validator", DefaultPrefix)
	} else if _, ok := pkVal.(record.PublicKeyValidator); !ok {
		return fmt.Errorf("protocol prefix %s must use the record.PublicKeyValidator for the /pk namespace", DefaultPrefix)
	}

	// 如果不存在 /ipns 命名空间验证器或者该验证器不是 ipns.Validator 类型，则返回错误信息
	if ipnsVal, ipnsValFound := nsval["ipns"]; !ipnsValFound {
		return fmt.Errorf("protocol prefix %s must support the /ipns namespaced Validator", DefaultPrefix)
	} else if _, ok := ipnsVal.(ipns.Validator); !ok {
		return fmt.Errorf("protocol prefix %s must use ipns.Validator for the /ipns namespace", DefaultPrefix)
	}
	return nil
}
