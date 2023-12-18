// dep2p

package dep2p

import (
	"bytes"
	"context"
	"encoding/gob"

	"github.com/bpfs/dep2p/kbucket"
	"github.com/bpfs/dep2p/streams"
	"github.com/bpfs/dep2p/utils"

	"github.com/libp2p/go-libp2p/config"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
)

type Options struct {
	Libp2pOpts       []config.Option // Option 是一个 libp2p 配置选项，可以提供给 libp2p 构造函数（`libp2p.New`）。
	DhtOpts          []Option        // Option 是一个 dht 配置选项，可以提供给 dht 构造函数(`dht.New`)
	BootstrapsPeers  []string        // 引导节点地址
	RendezvousString string          // 组名
	AddsBook         []string        // 地址簿
}

// Option 类型是一个函数，它接受一个 DeP2P 指针，并返回一个错误。它用于配置 DeP2P 结构体的选项。
type OptionDeP2P func(*DeP2P) error

type DeP2P struct {
	ctx            context.Context // ctx 是 dep2p 实例的上下文，用于在整个实例中传递取消信号和其他元数据。
	host           host.Host       // libp2p的主机对象。
	peerDHT        *DeP2PDHT        // 分布式哈希表。
	startUp        bool            // 是否启动标志。
	options        Options         // 存储libp2p 选项
	nodeInfo       *NodeInfo       // 节点信息
	connSupervisor *ConnSupervisor // 连接器
}

// New 创建新的 DeP2P 实例
func NewDeP2P(ctx context.Context, opts ...OptionDeP2P) (*DeP2P, error) {
	// 初始化 DeP2P 结构体
	deP2P := &DeP2P{
		ctx:      ctx,           // 上下文
		startUp:  false,         // 启动状态
		nodeInfo: GetNodeInfo(), // 获取节点信息（主机信息、硬盘信息、cpu信息、网络信息）
	}

	// 调用每个选项函数，并配置 DeP2PHost
	for _, opt := range opts {
		if err := opt(deP2P); err != nil {
			return nil, err
		}
	}

	// 使用配置后的 DeP2PHost 创建 libp2p 节点
	h, err := libp2p.New(deP2P.options.Libp2pOpts...)
	if err != nil {
		return nil, err
	}

	for _, addr := range h.Addrs() {
		logrus.Println("正在监听: ", addr)
	}

	logrus.Info("主机已创建: ", h.ID())

	deP2P.host = h

	// New 使用指定的主机和选项创建一个新的 DHT。 请注意，连接到 DHT 对等端并不一定意味着它也在 DHT 路由表中。
	// 如果路由表有超过“minRTRefreshThreshold”对等点，我们仅当我们成功从它获得查询响应或向我们发送查询时才将对等点视为路由表候选者。
	dhtDeP2P, err := New(ctx, h, deP2P.options.DhtOpts...)
	if err != nil {
		logrus.Errorf("DHT失败: %v", err)
		return nil, err

	}

	logrus.Info("引导DHT")
	// Bootstrap 告诉 DHT 进入满足 DeP2PRouter 接口的引导状态。
	if err := dhtDeP2P.Bootstrap(ctx); err != nil {
		logrus.Errorf("引导DHT失败: %v", err)
		return nil, err
	}

	deP2P.peerDHT = dhtDeP2P

	// 解析引导节点地址
	peerAddrInfos, err := utils.ParseAddrInfo(deP2P.options.AddsBook)
	if err != nil {
		return nil, err
	}

	// 创建新的连接监管器实例
	deP2P.connSupervisor = newConnSupervisor(deP2P.Host(), deP2P.ctx, deP2P.nodeInfo, deP2P.peerDHT, peerAddrInfos)

	// 创建启动信号
	readySignalC := make(chan struct{})
	// 连接已有节点
	deP2P.connSupervisor.startSupervising(readySignalC)
	// 主机启动发现节点
	if err := deP2P.networkDiscoveryNode(); err != nil {
		return nil, err
	}
	// 连接握手协议
	streams.RegisterStreamHandler(deP2P.host, HandshakeProtocol, streams.HandlerWithRW(deP2P.handshakeHandle))
	// 设置deP2P启动状态为true
	deP2P.startUp = true

	return deP2P, nil
}

// 接收方处理握手请求
func (bp *DeP2P) handshakeHandle(req *streams.RequestMessage, res *streams.ResponseMessage) error {
	res.Code = 200 // 响应代码
	res.Msg = "成功" // 响应消息

	handshakeres := &Handshake{
		ModeId:   int(bp.DHT().Mode()),
		NodeInfo: bp.nodeInfo,
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(handshakeres); err != nil {
		return err
	}

	// Mode 允许自省 DHT 的操作模式
	res.Data = buf.Bytes()

	res.Message.Sender = bp.Host().ID().String() // 发送方ID

	handshake := new(Handshake)

	slicePayloadBuffer := bytes.NewBuffer(req.Payload)
	gobDecoder := gob.NewDecoder(slicePayloadBuffer)
	if err := gobDecoder.Decode(handshake); err != nil {
		return err
	}

	table, exists := bp.connSupervisor.routingTables[handshake.ModeId]
	pidDecode, err := peer.Decode(req.Message.Sender)
	if err != nil {
		return err

	}

	if exists {

		// 键存在，可以使用 value
		// 更新Kbucket路由表
		renewKbucketRoutingTable(table, handshake.ModeId, pidDecode)

	} else {
		// 键不存在
		table, err := NewTable(bp.connSupervisor.host)
		if err != nil {
			logrus.Error("新建k桶失败", err)
		}
		// 更新Kbucket路由表
		renewKbucketRoutingTable(table, handshake.ModeId, pidDecode)
		//
		bp.connSupervisor.routingTables[handshake.ModeId] = table

	}

	return nil
}

// newConnSupervisor 创建一个新的 ConnSupervisor 实例
func newConnSupervisor(host host.Host, ctx context.Context, nodeInfo *NodeInfo, peerDHT *DeP2PDHT, peerAddrInfos []peer.AddrInfo) *ConnSupervisor {
	return &ConnSupervisor{
		host:          host,
		peerDHT:       peerDHT,
		nodeInfo:      nodeInfo,
		routingTables: make(map[int]*kbucket.RoutingTable),
		ctx:           ctx,
		peerAddrInfos: peerAddrInfos,
		startUp:       false,
		IsConnected:   false,
		actuators:     make(map[peer.ID]*tryToDialActuator),
	}
}

// Context 返回上下文
func (bp *DeP2P) Context() context.Context {
	return bp.ctx
}

// Host 返回 Host
func (bp *DeP2P) Host() host.Host {
	return bp.host
}

// RoutingTables 返回 RoutingTables
func (bp *DeP2P) RoutingTables() map[int]*kbucket.RoutingTable {
	return bp.connSupervisor.routingTables
}

// RoutingTable 根据dht类型获取指定类型RoutingTable
func (bp *DeP2P) RoutingTable(mode int) *kbucket.RoutingTable {

	routingTable, exists := bp.connSupervisor.routingTables[mode] // 获取键对应的值
	if exists {
		// 键存在，可以使用 value
		return routingTable
	} else {
		// 键不存在
		table, err := NewTable(bp.host)
		if err != nil {
			logrus.Error("新建k桶失败", err)
		}
		bp.connSupervisor.routingTables[mode] = table
		return table
	}
}

// NodeInfo 返回 节点信息
func (bp *DeP2P) NodeInfo() *NodeInfo {
	return bp.nodeInfo
}

// DHT 返回分布式哈希表
func (bp *DeP2P) DHT() *DeP2PDHT {
	return bp.peerDHT
}

// DHT 返回分布式哈希表
func (bp *DeP2P) ConnectPeer() []peer.AddrInfo {
	return bp.connSupervisor.ConnectedAddrInfo()
}

// IsRunning 当 Libp2p 已启动时返回true；否则返回false。
func (bp *DeP2P) IsRunning() bool {
	// bp.lock.RLock()
	// defer bp.lock.RUnlock()
	return bp.startUp
}

// Start 方法用于启动 Libp2p 主机。
func (bp *DeP2P) Start() error {
	return nil
}

// Stop 方法用于停止 Libp2p 主机。
func (bp *DeP2P) Stop() error {
	return bp.host.Close()
}

// Options 方法获取DeP2P 配置项
func (bp *DeP2P) Options() Options {
	return bp.options
}

// WithRendezvousString 设置本地网络监听地址
func WithRendezvousString(rendezvousString string) OptionDeP2P {

	return func(bp *DeP2P) error {
		bp.options.RendezvousString = rendezvousString
		return nil
	}
}

// WithLibp2pOpts 设置本地网络监听地址
func WithLibp2pOpts(Option []config.Option) OptionDeP2P {

	return func(bp *DeP2P) error {
		bp.options.Libp2pOpts = Option

		return nil
	}
}

// WithDhtOpts 设置本地网络监听地址
func WithDhtOpts(option []Option) OptionDeP2P {
	// baseOpts := []Option{}

	// baseOpts = append(baseOpts, Mode(m))

	return func(bp *DeP2P) error {
		bp.options.DhtOpts = option
		return nil
	}
}

// WithBootstrapsPeers 设置引导节点
func WithBootstrapsPeers(bootstrapsPeers []string) OptionDeP2P {

	return func(bp *DeP2P) error {
		bp.options.BootstrapsPeers = bootstrapsPeers
		return nil
	}
}
