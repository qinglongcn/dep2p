// kad-dht

package dep2p

import (
	"context"
	"fmt"
	"time"

	"github.com/bpfs/dep2p/internal"
	"github.com/bpfs/dep2p/metrics"

	"github.com/bpfs/dep2p/qpeerset"

	pb "github.com/bpfs/dep2p/pb"

	kb "github.com/bpfs/dep2p/kbucket"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

// GetClosestPeers 是一个 Kademlia 的 'node lookup' 操作。返回与给定键最接近的 K 个节点的通道。
//
// 如果上下文被取消，该函数将返回上下文错误以及迄今为止找到的最接近的 K 个节点。
func (dht *DeP2PDHT) GetClosestPeers(ctx context.Context, key string) ([]peer.ID, error) {
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.GetClosestPeers", trace.WithAttributes(internal.KeyAsAttribute("Key", key)))
	defer span.End()

	if key == "" {
		return nil, fmt.Errorf("can't lookup empty key")
	}

	// TODO: 我可以打破接口！返回 []peer.ID
	lookupRes, err := dht.runLookupWithFollowup(ctx, key, dht.pmGetClosestPeers(key), func(*qpeerset.QueryPeerset) bool { return false })

	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil || !lookupRes.completed {
		return lookupRes.peers, err
	}

	// 用于网络大小估算的查找结果跟踪
	if err = dht.nsEstimator.Track(key, lookupRes.closest); err != nil {
		logrus.Debugf("network size estimator track peers: %s", err)
	}

	// 网络大小估算器。
	// NetworkSize 方法用于计算当前网络规模的估计值。
	if ns, err := dht.nsEstimator.NetworkSize(); err == nil {
		// Measures 性能指标——网络规模估计
		// M 创建一个新的 int64 测量值。使用 Record 来记录测量结果。
		metrics.NetworkSize.M(int64(ns))
	}

	// 刷新该键的 cpl，因为查询成功了
	dht.routingTable.ResetCplRefreshedAtForID(kb.ConvertKey(key), time.Now())

	return lookupRes.peers, nil
}

// pmGetClosestPeers 是 GetClosestPeer 查询函数的协议传递版本。
func (dht *DeP2PDHT) pmGetClosestPeers(key string) queryFn {
	return func(ctx context.Context, p peer.ID) ([]*peer.AddrInfo, error) {
		// 对于 DHT 查询命令
		routing.PublishQueryEvent(ctx, &routing.QueryEvent{
			Type: routing.SendingQuery,
			ID:   p,
		})

		//////////////////////////////////////////////////////////////////////
		var val pb.Message_ClosestPeers
		val.Self = []byte(dht.self) // 当前节点的 pper ID
		val.Mode = int32(dht.mode)  // 当前运行模式
		valByte, err := val.Marshal()
		if err != nil {
			return nil, err
		}
		//////////////////////////////////////////////////////////////////////

		// GetClosestPeers 要求对等点返回 XOR 空间中最接近 id 的 K（DHT 范围参数）DHT 服务器对等点
		// 注意：如果peer碰巧知道另一个peerID与给定id完全匹配的peer，它将返回该peer，即使该peer不是DHT服务器节点。
		peers, err := dht.protoMessenger.GetClosestPeers(ctx, p, peer.ID(key), valByte)
		if err != nil {
			logrus.Debugf("error getting closer peers: %s", err)
			routing.PublishQueryEvent(ctx, &routing.QueryEvent{
				Type:  routing.QueryError,
				ID:    p,
				Extra: err.Error(),
			})
			return nil, err
		}

		// 对于 DHT 查询命令
		routing.PublishQueryEvent(ctx, &routing.QueryEvent{
			Type:      routing.PeerResponse,
			ID:        p,
			Responses: peers,
		})

		return peers, err
	}
}
