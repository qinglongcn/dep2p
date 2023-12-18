// dep2p

package dep2p

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/config"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"

	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/sirupsen/logrus"

	"github.com/libp2p/go-libp2p/core/host"
)

// go test -v -bench=. -benchtime=10m ./... -run TestNewDeP2P
func TestNewDeP2P(t *testing.T) {

	// 创建DeP2P node01
	node01, err := makeDeP2P()
	if err != nil {
		panic(err)
	}
	logrus.Printf("node01[%s] 启动\n", node01.Host().ID())

	// 创建DeP2P node02
	node02, err := makeDeP2P()
	if err != nil {
		panic(err)
	}
	logrus.Printf("node02[%s] 启动\n", node02.Host().ID())

	// 连接两个主机
	// if err := connectNode(t, node01.Host(), node02.Host()); err != nil {
	// 	panic(err)
	// }

	// 等待一段时间以确保可以发现节点
	time.Sleep(time.Second * 1)

	select {}
}

// 创建 DeP2P 网络对象
func makeDeP2P() (*DeP2P, error) {

	// 创建上下文
	ctx := context.Background()

	// 生成一个随机的私钥
	// 这是一个硬编码的 RSA 私钥
	privKey, err := generateIdentity()
	if err != nil {
		fmt.Println("获取密钥失败:", err)
		panic(err)
	}
	// 获取空闲端口号（测试用）
	port, err := getFreePort()
	if err != nil {
		fmt.Println("获取空闲端口号失败:", err)
		panic(err)
	}

	bp, err := NewDeP2P(ctx,
		WithLibp2pOpts(buildHostOptions(nil, privKey, port)), // 设置libp2p 选项
		WithDhtOpts(buildDHTOptions(ModeServer)),             // 设置dht配置选线
		WithRendezvousString("rendezvous:wesign.xyz.02"),
	)
	// 判断是否失败
	if err != nil {
		return nil, err
	}

	logrus.Printf("==>\t%s", bp.host.ID().String())
	// logrus.Printf("==>\t%v", bp.peerDHT.RoutingTable().Print())
	//bp.peerDHT.RoutingTable().Print()
	return bp, err
}
func buildDHTOptions(m ModeOpt) []Option {
	baseOpts := []Option{}

	baseOpts = append(baseOpts, Mode(m))
	return baseOpts
}

// 获取空闲端口号
func getFreePort() (string, error) {
	// 监听一个未指定端口的TCP地址
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", err
	}
	defer listener.Close()

	// 获取监听的地址
	address := listener.Addr().(*net.TCPAddr)
	port := strconv.Itoa(address.Port)

	return port, nil
}

// 生成私钥
func generateIdentity() (identity crypto.PrivKey, err error) {
	r := crand.Reader

	identity, _, err = crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	return identity, err
}

// connectNode 函数连接两个主机。
func connectNode(t *testing.T, a, b host.Host) error {
	pinfo := a.Peerstore().PeerInfo(a.ID())       // 获取主机a的PeerInfo
	err := b.Connect(context.Background(), pinfo) // 让主机b连接到主机a

	if err != nil {

		return err
	}
	return nil
}

const (

	// DefaultConnMgrHighWater 是连接管理器'high water'标记的默认值
	DefaultConnMgrHighWater = 96

	// DefaultConnMgrLowWater 是连接管理器'low water'标记的默认值
	DefaultConnMgrLowWater = 32

	// DefaultConnMgrGracePeriod 是连接管理器宽限期的默认值
	DefaultConnMgrGracePeriod = time.Second * 20
)

// 构建主机选项
func buildHostOptions(psk pnet.PSK, sk crypto.PrivKey, portNumber string) []config.Option {
	// IPFS配置
	grace := DefaultConnMgrGracePeriod
	low := int(DefaultConnMgrLowWater)
	high := int(DefaultConnMgrHighWater)

	// NewConnManager 使用提供的参数创建一个新的 BasicConnMgr：lo 和 hi 是管理将维护的连接数量的水印。
	// 当对等点计数超过'high water'时，许多对等点将被修剪（并且它们的连接终止），直到保留'low water'对等点。
	cm, err := connmgr.NewConnManager(low, high, connmgr.WithGracePeriod(grace))
	if err != nil {
		logrus.Errorf("初始化cm节点%v", err)
	}

	// NewPeerstore 创建内存中线程安全的对等点集合。
	// 调用者有责任调用RemovePeer以确保peerstore的内存消耗不会无限制地增长。
	libp2pPeerstore, err := pstoremem.NewPeerstore()
	if err != nil {
		logrus.Errorf("初始化存储节点%v", err)
	}

	// rcmgrObs.MustRegisterWith(prometheus.DefaultRegisterer) 注册 rcmgrObs 对象到 Prometheus 的默认注册器中，
	// 以便将其暴露为 Prometheus 指标。
	//rcmgrObs.MustRegisterWith(prometheus.DefaultRegisterer)

	// str 是一个 StatsTraceReporter 对象，用于收集和报告资源管理器的统计信息和追踪数据。
	// rcmgrObs.NewStatsTraceReporter() 用于创建 StatsTraceReporter 对象。
	// 如果创建过程中发生错误，则将错误记录下来并终止程序的执行。
	str, err := rcmgr.NewStatsTraceReporter()
	if err != nil {
		logrus.Errorf("初始化rcmgrObs%v", err)
	}

	// rmgr 是一个资源管理器对象，用于管理和分配资源。
	// rcmgr.NewResourceManager() 用于创建资源管理器对象，参数包括资源限制器和追踪报告器等选项。
	// 如果创建过程中发生错误，则将错误记录下来并终止程序的执行。
	rmgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(rcmgr.DefaultLimits.AutoScale()), rcmgr.WithTraceReporter(str))
	if err != nil {
		logrus.Fatal(err)
	}
	// 默认中继配置选项
	def := relay.DefaultResources()
	options := []libp2p.Option{
		// Peerstore 配置 libp2p 以使用给定的对等存储。
		libp2p.Peerstore(libp2pPeerstore),
		// Ping 会配置libp2p 来支持ping 服务； 默认启用。
		libp2p.Ping(false),
		libp2p.Identity(sk),
		// DefaultSecurity 是默认的安全选项。
		// 当您想要扩展而不是替换受支持的传输安全协议时非常有用。
		libp2p.DefaultSecurity,
		// ConnectionManager 将 libp2p 配置为使用给定的连接管理器。
		libp2p.ConnectionManager(cm),
		// ResourceManager 将 libp2p 配置为使用给定的 ResourceManager。
		// 当使用 ResourceManager 接口的 p2p/host/resource-manager 实现时，建议通过调用 SetDefaultServiceLimits 设置 libp2p 协议的限制。
		libp2p.ResourceManager(rmgr),
		// 对于大文件传输，选择合适的传输协议很重要。您可以尝试使用 QUIC（基于 UDP 的传输协议），因为它具有低延迟、高并发和连接迁移等优点。
		// libp2p.Transport(quic.NewTransport),
		// TODO: 需要根据新包优化
		// 使用 QUIC（基于 UDP 的传输协议）并设置连接超时
		// libp2p.Transport(func(u *tptu.Upgrader) *quic.Transport {
		//  t := quic.NewTransport(u)
		//  t.SetHandshakeTimeout(5 * time.Minute) // 设置为 5 分钟，您可以根据需求调整这个值
		//  return t
		// }),
		// Muxer 配置 libp2p 以使用给定的流多路复用器。 name 是协议名称。
		libp2p.Muxer("/yamux/1.0.0", yamux.DefaultTransport), // 添加 yamux 传输协议
		// 尝试使用 uPNP 为 NATed 主机打开端口。
		libp2p.NATPortMap(),

		// EnableRelay 配置 libp2p 以启用中继传输。
		libp2p.EnableRelay(),
		// 实验性 EnableHolePunching 通过启用 NATT 的对等点来启动和响应打孔尝试以创建与其他对等点的直接/NAT 遍历连接来启用 NAT 遍历。
		libp2p.EnableHolePunching(),
		// EnableNATService 将 libp2p 配置为向对等点提供服务以确定其可达性状态。
		libp2p.EnableNATService(),
		// 启用中继服务
		libp2p.EnableRelayService(
			relay.WithResources(
				relay.Resources{
					Limit: &relay.RelayLimit{
						Data:     def.Limit.Data,     // 128K，设置每个中继数据的限制为128K
						Duration: def.Limit.Duration, // 设置每个中继的持续时间为2分钟
					},
					MaxCircuits:            def.MaxCircuits,            // 设置最大的中继电路数量为16个
					BufferSize:             def.BufferSize,             // 缓冲区大小由2048（2kb）调整为20480（20kb）
					ReservationTTL:         def.ReservationTTL,         // 设置中继预留的生存时间为1小时
					MaxReservations:        def.MaxReservations,        // 设置最大中继预留数量为128个
					MaxReservationsPerIP:   def.MaxReservationsPerIP,   // 设置每个IP地址最大的中继预留数量为8个
					MaxReservationsPerPeer: def.MaxReservationsPerPeer, // 设置每个对等节点最大的中继预留数量为4个
					MaxReservationsPerASN:  def.MaxReservationsPerASN,  // 设置每个ASN（自治系统号）最大的中继预留数量为32个
				},
			),
		),
	}
	// 如果是中继节点指定host主机端口
	if portNumber != "" {
		// ListenAddrStrings 配置 libp2p 来监听给定的（未解析的）地址。
		options = append(options, libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", portNumber)))
		// Transport 将 libp2p 配置为使用给定的传输（或传输构造函数）。
		options = append(options, libp2p.Transport(tcp.NewTCPTransport, tcp.WithMetrics()))
		// ForceReachabilityPublic 覆盖了AutoNAT子系统中的自动可达性检测，迫使本地节点相信它是可以从外部到达的。
		options = append(options, libp2p.ForceReachabilityPublic())
	} else {
		// ForceReachabilityPrivate 覆盖 AutoNAT 子系统中的自动可达性检测，强制本地节点相信它在 NAT 后面并且无法从外部访问。
		options = append(options, libp2p.ForceReachabilityPrivate())
	}

	// 私有网络
	if psk != nil {
		options = append(options, []libp2p.Option{
			// PrivateNetwork 将 libp2p 配置为使用给定的专用网络保护器。
			libp2p.PrivateNetwork(psk),
		}...)
	}

	return options
}
