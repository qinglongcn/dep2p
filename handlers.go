// kad-dht

package dep2p

import (
	"context"
	"fmt"

	pb "github.com/bpfs/dep2p/pb"

	pstore "github.com/libp2p/go-libp2p/p2p/host/peerstore"
	"github.com/sirupsen/logrus"

	"github.com/libp2p/go-libp2p/core/peer"
)

// dhthandler 指定处理 DHT 消息的函数的签名
type dhtHandler func(context.Context, peer.ID, *pb.Message) (*pb.Message, error)

// handlerForMsgType 返回与给定消息类型对应的处理函数
func (dht *DeP2PDHT) handlerForMsgType(t pb.Message_MessageType) dhtHandler {
	switch t {
	// FIND_NODE 消息类型，用于查找网络中的某个节点
	case pb.Message_FIND_NODE:
		// handleFindPeer 函数用于处理查找对等方的请求
		return dht.handleFindPeer
	// PING 消息类型，用于检查另一个节点是否在线
	case pb.Message_PING:
		// handlePing 函数用于处理 ping 请求
		return dht.handlePing
	}

	// 如果启用了值存储，则处理 GET_VALUE 和 PUT_VALUE 消息类型
	// if dht.enableValues {
	// 	switch t {
	// 	// GET_VALUE 消息类型，用于从 DHT 中获取一个键的值。
	// 	case pb.Message_GET_VALUE:
	// 		// handleGetValue 处理 GET_VALUE 消息类型。
	// 		return dht.handleGetValue
	// 		// PUT_VALUE 消息类型，用于在 DHT 中设置一个键值对。
	// 	case pb.Message_PUT_VALUE:
	// 		// handlePutValue 函数用于处理存储值的请求
	// 		return dht.handlePutValue
	// 	}
	// }

	// 如果启用了提供者功能，则处理 ADD_PROVIDER 和 GET_PROVIDERS 消息类型
	// if dht.enableProviders {
	// 	switch t {
	// 	// ADD_PROVIDER 消息类型，表示一个节点提供了某资源。
	// 	case pb.Message_ADD_PROVIDER:
	// 		// handleAddProvider 函数用于处理添加提供者的请求
	// 		return dht.handleAddProvider
	// 		// GET_PROVIDERS 消息类型，用于查询提供某个资源的节点。
	// 	case pb.Message_GET_PROVIDERS:
	// 		// handleGetProviders 函数用于处理获取提供者的请求
	// 		return dht.handleGetProviders
	// 	}
	// }

	return nil
}

// handlePing 函数用于处理 ping 请求
func (dht *DeP2PDHT) handlePing(_ context.Context, p peer.ID, pmes *pb.Message) (*pb.Message, error) {
	// 打印日志，回应来自对等方的 ping 消息
	logrus.Debugf("%s Responding to ping from %s!\n", dht.self, p)
	return pmes, nil
}

// handleFindPeer 函数用于处理查找对等方的请求
func (dht *DeP2PDHT) handleFindPeer(ctx context.Context, from peer.ID, pmes *pb.Message) (_ *pb.Message, _err error) {
	// NewMessage 构造一个具有给定类型、键和级别的新的 DHT 消息
	resp := pb.NewMessage(pmes.GetType(), nil, nil, pmes.GetClusterLevel())
	var closest []peer.ID

	if len(pmes.GetKey()) == 0 {
		return nil, fmt.Errorf("handleFindPeer with empty key")
	}

	// 如果寻找自我...特殊情况，我们将其发送到 CloserPeers。
	targetPid := peer.ID(pmes.GetKey())
	// logs.Printf("============= 2-0 ============= \t %s\t %s\t %s", dht.host.ID().String(), targetPid, string(pmes.GetValue()))
	if targetPid == dht.self {
		closest = []peer.ID{dht.self}
	} else {
		// logs.Printf("==== 2-0 ====\t %s\t %s\t %s", dht.self.String(), targetPid, string(pmes.GetValue()))
		//logs.Printf("==== 2-0 ====\t %s\t %s", dht.self.String(), targetPid)

		// betterPeersToQuery 返回 nearestPeersToQuery 的结果，并进行了一些额外的过滤。
		closest = dht.betterPeersToQuery(pmes, from, dht.bucketSize)

		// 切勿向同伴透露自己的情况。
		if targetPid != from {
			// 如果目标对等点尚未出现在我们的路由表中，请将其添加到最接近的对等点集合中。
			//
			// 稍后，当我们查找该组中所有对等点的已知地址时，如果我们_实际上_不知道它在哪里，我们将修剪该对等点。
			found := false
			for _, p := range closest {
				if targetPid == p {
					found = true
					break
				}
			}
			if !found {
				closest = append(closest, targetPid)
			}
		}
	}

	if closest == nil {
		return resp, nil
	}

	// TODO: pstore.PeerInfos 应该移到核心 (=> peerstore.AddrInfos).
	closestinfos := pstore.PeerInfos(dht.peerstore, closest)
	// 可能会分配过多的空间，但这个数组是临时的
	withAddresses := make([]peer.AddrInfo, 0, len(closestinfos))
	for _, pi := range closestinfos {
		if len(pi.Addrs) > 0 {
			withAddresses = append(withAddresses, pi)
		}
	}

	// PeerInfosToPBPeers 将给定的 []peer.AddrInfo 转换为 []*Message_Peer，可以写入消息并发送出去。
	// 除了执行 PeersToPBPeers 的操作外，此函数还使用给定的 network.Network 设置 ConnectionType。
	resp.CloserPeers = pb.PeerInfosToPBPeers(dht.host.Network(), withAddresses)
	return resp, nil
}
