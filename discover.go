// dep2p

package dep2p

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"sync"
	"time"

	"github.com/bpfs/dep2p/kbucket"
	"github.com/bpfs/dep2p/streams"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	hostpeerstore "github.com/libp2p/go-libp2p/p2p/host/peerstore"
	"github.com/multiformats/go-multiaddr"

	"github.com/sirupsen/logrus"
)

// 连接握手协议
const (
	// HandshakeProtocol 默认 dep2p 连接握手协议
	HandshakeProtocol = "/dep2p/handshake/1.0.0"
)

// networkDiscoveryNode 用于在网络发现节点
func (bp *DeP2P) networkDiscoveryNode() error {
	// 连接至引导节点
	if err := connectToBootstrapPeers(bp.Context(), bp.Host(), bp.options.BootstrapsPeers); err != nil {
		return err
	}

	logrus.Info("宣布我们自己...")
	routingDiscovery := NewRoutingDiscovery(bp.peerDHT)
	dutil.Advertise(bp.ctx, routingDiscovery, bp.options.RendezvousString)
	logrus.Info("发布成功!")
	logrus.Printf("组名%s", bp.options.RendezvousString)
	go func() {
		// defer func() {
		// 	if err := recover(); err != nil {
		// 		logrus.Errorf("[ConnSupervisor.handleChanNewPeerFound] 恢复错误，%s", err)
		// 	}
		// }()
		for {
			peerChan, err := routingDiscovery.FindPeers(bp.ctx, bp.options.RendezvousString)
			if err != nil {
				logrus.Errorf("寻找其他节点失败: %v", err)
				continue
			}

			bp.connSupervisor.handleChanNewPeerFound(peerChan)
		}
	}()

	return nil
}

// 连接至自配引导节点
func connectToBootstrapPeers(ctx context.Context, host host.Host, bootstrapPeers []string) error {
	var defaultMultiaddrs []multiaddr.Multiaddr

	// 解析引导节点的 Multiaddr
	for _, peer := range bootstrapPeers {
		maddr, err := multiaddr.NewMultiaddr(peer)
		if err == nil {
			defaultMultiaddrs = append(defaultMultiaddrs, maddr)
		}
	}

	// 获取默认的引导节点
	defaultBootstrapPeers := DefaultBootstrapPeers
	defaultBootstrapPeers = append(defaultBootstrapPeers, defaultMultiaddrs...)

	var wg sync.WaitGroup
	successfulConnection := false

	for _, peerAddr := range defaultBootstrapPeers {
		// 将 Multiaddr 转换为 AddrInfo
		peerInfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			logrus.Errorf("地址 %s 转换失败: %v", peerAddr.String(), err)
			continue
		}

		wg.Add(1)
		go func(peerInfo peer.AddrInfo) {
			defer wg.Done()
			if err := host.Connect(ctx, peerInfo); err != nil {
				logrus.Debugf("连接引导节点警告: %s", err.Error())
			} else {
				logrus.Printf("连接引导节点成功: %s", peerInfo.ID)
				successfulConnection = true
			}
		}(*peerInfo)
	}

	wg.Wait() // 阻塞，确保所有的协程全部返回

	if !successfulConnection {
		logrus.Errorf("未能连接至引导节点")
		return fmt.Errorf("未能连接至引导节点")
	}

	return nil
}

// handleChanNewPeerFound 处理从发现中获取到的新对等节点。
func (cs *ConnSupervisor) handleChanNewPeerFound(peerChan <-chan peer.AddrInfo) {

	for p := range peerChan {
		cs.tryConnectLock.Lock() // 加锁以确保线程安全
		if p.ID == cs.host.ID() {
			cs.tryConnectLock.Unlock() // 如果对等节点已连接或者是当前节点自身，则解锁并继续下一次循环
			continue
		}

		// 判断是否已经连接过
		if !cs.Connected(p) {
			// AddAddrs 使用给定的 ttl（生存时间）提供此 AddrBook 地址以供使用，之后该地址不再有效。 如果管理器的 TTL 更长，则该操作对该地址是空操作
			cs.host.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)

			handshake, err := handshakeAgreement(cs.ctx, cs.host, cs.nodeInfo, p.ID, HandshakeProtocol, int(cs.peerDHT.Mode()))

			if err != nil {
				// 如果错误继续下一个同行
				// TODO:后面补充节点信息
				cs.AddConn(p, false, -1, nil) // 将新的连接添加到连接管理器中
				cs.tryConnectLock.Unlock()    // 如果对等节点已连接或者是当前节点自身，则解锁并继续下一次循环
				continue
			}

			table, exists := cs.routingTables[handshake.ModeId]
			if exists {
				// 键存在，可以使用 value
				// 更新Kbucket路由表
				renewKbucketRoutingTable(table, handshake.ModeId, p.ID)

			} else {
				// 键不存在
				table, err := NewTable(cs.host)
				if err != nil {
					logrus.Error("新建k桶失败", err)
				}
				// 更新Kbucket路由表
				renewKbucketRoutingTable(table, handshake.ModeId, p.ID)
				//
				cs.routingTables[handshake.ModeId] = table

			}

			logrus.Info("连接成功", p.ID)
			// TODO:后面补充节点信息
			cs.AddConn(p, true, handshake.ModeId, handshake.NodeInfo) // 将新的连接添加到连接管理器中

			cs.tryConnectLock.Unlock() // 解锁
			continue
		}

		cs.tryConnectLock.Unlock() // 解锁

	}

}

// 新建路由表
func NewTable(h host.Host) (table *kbucket.RoutingTable, err error) {
	var NoOpThreshold = 100 * time.Hour
	m := hostpeerstore.NewMetrics()

	// NewRoutingTable 使用给定的桶大小、本地 ID 和延迟容限创建一个新的路由表。
	routingTable, err := kbucket.NewRoutingTable(50, kbucket.ConvertPeerID(h.ID()), time.Hour, m, NoOpThreshold, nil)
	if err != nil {
		logrus.Errorf("自定义路由表失败: %v", err)
		return nil, err
	}

	return routingTable, nil
}

// 更新Kbucket路由表
func renewKbucketRoutingTable(routingTable *kbucket.RoutingTable, mode int, pi peer.ID) {
	// TryAddPeer 如果对等方是新添加到路由表的，则返回一个布尔值设置为 true，否则返回 false。
	b, err := routingTable.TryAddPeer(pi, mode, true, false)
	if err != nil {
		if err.Error() == "peer rejected; insufficient capacity" {
			logrus.Errorf("节点能力不足被拒绝")
		} else {
			logrus.Errorf("节点 %s 添加至K桶失败: %v", pi, err)
		}
	}

	if b {
		var t time.Time
		// UpdateLastSuccessfulOutboundQueryAt 更新对端的 LastSuccessfulOutboundQueryAt 时间。 如果更新成功则返回 true，否则返回 false。
		routingTable.UpdateLastSuccessfulOutboundQueryAt(pi, t)
		// UpdateLastUsefulAt 更新对端的 LastUsefulAt 时间。 如果更新成功则返回 true，否则返回 false。
		routingTable.UpdateLastUsefulAt(pi, t)
	}

}

// 握手协议
type Handshake struct {
	ModeId   int       // 模式 1客户端2服务器
	NodeInfo *NodeInfo // 当前节点信息
}

// 握手协议
func handshakeAgreement(ctx context.Context, h host.Host, nodeInfo *NodeInfo, pid peer.ID, ptl string, modeId int) (*Handshake, error) {
	// 准备请求消息

	// payload := &struct {
	// 	PeerAddrInfo []peer.AddrInfo // 当前节点连接的信息
	// 	NodeInfo     NodeInfo        // 当前节点信息
	// }{
	// 	PeerAddrInfo: peerBook,
	// 	NodeInfo:     nodeInfo,
	// }
	handshake := &Handshake{
		ModeId:   modeId,
		NodeInfo: nodeInfo,
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(handshake); err != nil {
		return nil, err
	}

	// modeByes := utils.DigitToByte(modeId) // 响应数据

	request := &streams.RequestMessage{
		Message: &streams.Message{
			Sender:   h.ID().String(), // 发送方ID
			Receiver: pid.String(),    // 接收方ID
		},
		Payload: buf.Bytes(),
	}

	// 序列化请求
	reqByte, err := request.Marshal()
	if err != nil {
		return nil, err
	}

	stream, err := h.NewStream(ctx, pid, protocol.ID(ptl))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(time.Second * 8))

	// 将消息写入流
	if err = streams.WriteStream(reqByte, stream); err != nil {
		return nil, err
	}

	// 从流中读取消息
	responseByte, err := streams.ReadStream(stream)
	if err != nil {
		return nil, err
	}

	var response streams.ResponseMessage

	// 反序列化响应
	err = response.Unmarshal(responseByte)
	if err != nil || response.Code != 200 {
		return nil, err
	}

	handshake1 := new(Handshake)

	slicePayloadBuffer := bytes.NewBuffer(response.Data)
	gobDecoder := gob.NewDecoder(slicePayloadBuffer)
	if err := gobDecoder.Decode(handshake1); err != nil {
		return nil, err
	}

	return handshake1, nil
}
