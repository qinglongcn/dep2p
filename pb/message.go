package dht_pb

import (
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"

	ma "github.com/multiformats/go-multiaddr"
)

// PeerRoutingInfo 结构体定义了对等节点的路由信息
type PeerRoutingInfo struct {
	peer.AddrInfo
	network.Connectedness
}

// NewMessage 构造一个具有给定类型、键和级别的新的 DHT 消息
func NewMessage(typ Message_MessageType, key, value []byte, level int) *Message {
	m := &Message{
		Type:  typ,   // 定义它是什么类型的消息。
		Key:   key,   // 用于指定与该消息关联的密钥。
		Value: value, // 用于传输附加值
	}
	// SetClusterLevel 调整并设置消息中的集群级别。 需要进行 +/- 1 的调整以区分有效的第一级 (1) 和默认的 "无值" 的 protobuf 行为 (0)。
	m.SetClusterLevel(level)
	return m
}

// peerRoutingInfoToPBPeer 将 PeerRoutingInfo 转换为 Message_Peer
func peerRoutingInfoToPBPeer(p PeerRoutingInfo) Message_Peer {
	var pbp Message_Peer

	// 初始化 pbp 的 Addrs 切片
	pbp.Addrs = make([][]byte, len(p.Addrs))

	// 遍历 p.Addrs，将每个 maddr 转换为字节切片并存储在 pbp.Addrs 中
	for i, maddr := range p.Addrs {
		pbp.Addrs[i] = maddr.Bytes() // Bytes，而不是 String。压缩的。
	}

	// 设置 pbp 的 Id 字段为 byteString(p.ID)
	pbp.Id = byteString(p.ID)

	// 设置 pbp 的 Connection 字段为 ConnectionType(p.Connectedness)
	pbp.Connection = ConnectionType(p.Connectedness)

	return pbp
}

// peerInfoToPBPeer 将 peer.AddrInfo 转换为 Message_Peer
func peerInfoToPBPeer(p peer.AddrInfo) Message_Peer {
	var pbp Message_Peer

	// 初始化 pbp 的 Addrs 切片
	pbp.Addrs = make([][]byte, len(p.Addrs))

	// 遍历 p.Addrs，将每个 maddr 转换为字节切片并存储在 pbp.Addrs 中
	for i, maddr := range p.Addrs {
		pbp.Addrs[i] = maddr.Bytes() // Bytes，而不是 String。压缩的。
	}

	// 设置 pbp 的 Id 字段为 byteString(p.ID)
	pbp.Id = byteString(p.ID)

	return pbp
}

// PBPeerToPeerInfo 将 *Message_Peer 转换为其 peer.AddrInfo 对应项
func PBPeerToPeerInfo(pbp Message_Peer) peer.AddrInfo {
	return peer.AddrInfo{
		ID:    peer.ID(pbp.Id),
		Addrs: pbp.Addresses(),
	}
}

// RawPeerInfosToPBPeers 将一组 Peers 转换为一组 *Message_Peer，准备发送出去
func RawPeerInfosToPBPeers(peers []peer.AddrInfo) []Message_Peer {
	pbpeers := make([]Message_Peer, len(peers))
	for i, p := range peers {
		pbpeers[i] = peerInfoToPBPeer(p)
	}
	return pbpeers
}

// PeerInfosToPBPeers 将给定的 []peer.AddrInfo 转换为 []*Message_Peer，可以写入消息并发送出去。
// 除了执行 PeersToPBPeers 的操作外，此函数还使用给定的 network.Network 设置 ConnectionType。
func PeerInfosToPBPeers(n network.Network, peers []peer.AddrInfo) []Message_Peer {
	pbps := RawPeerInfosToPBPeers(peers)
	for i, pbp := range pbps {
		c := ConnectionType(n.Connectedness(peers[i].ID))
		pbp.Connection = c
	}
	return pbps
}

// PeerRoutingInfosToPBPeers 将给定的 []PeerRoutingInfo 转换为 []Message_Peer
func PeerRoutingInfosToPBPeers(peers []PeerRoutingInfo) []Message_Peer {
	pbpeers := make([]Message_Peer, len(peers))
	for i, p := range peers {
		pbpeers[i] = peerRoutingInfoToPBPeer(p)
	}
	return pbpeers
}

// PBPeersToPeerInfos 将给定的 []*Message_Peer 转换为 []peer.AddrInfo
// 无效的地址将被静默忽略。
func PBPeersToPeerInfos(pbps []Message_Peer) []*peer.AddrInfo {
	peers := make([]*peer.AddrInfo, 0, len(pbps))
	for _, pbp := range pbps {
		ai := PBPeerToPeerInfo(pbp)
		peers = append(peers, &ai)
	}
	return peers
}

// Addresses 返回与 Message_Peer 条目关联的 multiaddr
func (m *Message_Peer) Addresses() []ma.Multiaddr {
	if m == nil {
		return nil
	}

	maddrs := make([]ma.Multiaddr, 0, len(m.Addrs))
	for _, addr := range m.Addrs {
		maddr, err := ma.NewMultiaddrBytes(addr)
		if err != nil {
			logrus.Debugln("解码 peer 的 multiaddr 时出错", "peer", peer.ID(m.Id), "错误", err)
			continue
		}

		maddrs = append(maddrs, maddr)
	}
	return maddrs
}

// GetClusterLevel 获取并调整消息中的集群级别。
// 需要进行 +/- 1 的调整以区分有效的第一级 (1) 和默认的 "无值" 的 protobuf 行为 (0)。
func (m *Message) GetClusterLevel() int {
	level := m.GetClusterLevelRaw() - 1
	if level < 0 {
		return 0
	}
	return int(level)
}

// SetClusterLevel 调整并设置消息中的集群级别。
// 需要进行 +/- 1 的调整以区分有效的第一级 (1) 和默认的 "无值" 的 protobuf 行为 (0)。
func (m *Message) SetClusterLevel(level int) {
	lvl := int32(level)
	m.ClusterLevelRaw = lvl + 1
}

// ConnectionType 返回与 network.Connectedness 关联的 Message_ConnectionType。
func ConnectionType(c network.Connectedness) Message_ConnectionType {
	switch c {
	default:
		return Message_NOT_CONNECTED
	case network.NotConnected:
		return Message_NOT_CONNECTED
	case network.Connected:
		return Message_CONNECTED
	case network.CanConnect:
		return Message_CAN_CONNECT
	case network.CannotConnect:
		return Message_CANNOT_CONNECT
	}
}

// Connectedness 函数返回与 Message_ConnectionType 关联的 network.Connectedness。
func Connectedness(c Message_ConnectionType) network.Connectedness {
	switch c {
	default:
		return network.NotConnected
	case Message_NOT_CONNECTED:
		return network.NotConnected
	case Message_CONNECTED:
		return network.Connected
	case Message_CAN_CONNECT:
		return network.CanConnect
	case Message_CANNOT_CONNECT:
		return network.CannotConnect
	}
}
