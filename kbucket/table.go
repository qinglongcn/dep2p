// Package kbucket implements a kademlia 'k-bucket' routing table.
package kbucket

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/sirupsen/logrus"

	"github.com/bpfs/dep2p/kbucket/peerdiversity"
)

// ErrPeerRejectedHighLatency：表示对等节点被拒绝的错误，原因是延迟太高。
var ErrPeerRejectedHighLatency = fmt.Errorf("peer rejected; latency too high")

// ErrPeerRejectedNoCapacity：表示对等节点被拒绝的错误，原因是容量不足。
var ErrPeerRejectedNoCapacity = fmt.Errorf("peer rejected; insufficient capacity")

// RoutingTable 定义了路由表。
type RoutingTable struct {
	ctx        context.Context    // 路由表的上下文
	ctxCancel  context.CancelFunc // 用于取消 RT 上下文的函数
	local      ID                 // 本地对等节点的 ID
	tabLock    sync.RWMutex       // 总体锁，为了性能优化会进行细化
	metrics    peerstore.Metrics  // 延迟指标
	maxLatency time.Duration      // 该集群中对等节点的最大可接受延迟
	// kBuckets 定义了与其他节点的所有联系。
	buckets        []*bucket          // 存储与其他节点的联系的桶（buckets）。
	bucketsize     int                // 桶的大小。
	cplRefreshLk   sync.RWMutex       // 用于刷新 Cpl 的锁。
	cplRefreshedAt map[uint]time.Time // 存储每个 Cpl 的刷新时间。
	// 通知函数
	PeerRemoved           func(peer.ID)         // 对等节点被移除时的通知函数。
	PeerAdded             func(peer.ID)         //  对等节点被添加时的通知函数。
	usefulnessGracePeriod time.Duration         // usefulnessGracePeriod 是我们给予桶中对等节点的最大宽限期，如果在此期限内对我们没有用处，我们将将其驱逐以为新对等节点腾出位置（如果桶已满）
	df                    *peerdiversity.Filter // 对等节点多样性过滤器。
}

// NewRoutingTable 使用给定的桶大小、本地 ID 和延迟容忍度创建一个新的路由表。
func NewRoutingTable(bucketsize int, localID ID, latency time.Duration, m peerstore.Metrics, usefulnessGracePeriod time.Duration,
	df *peerdiversity.Filter) (*RoutingTable, error) {
	rt := &RoutingTable{
		buckets:    []*bucket{newBucket()},
		bucketsize: bucketsize,
		local:      localID,

		maxLatency: latency,
		metrics:    m,

		cplRefreshedAt: make(map[uint]time.Time),

		PeerRemoved: func(peer.ID) {},
		PeerAdded:   func(peer.ID) {},

		usefulnessGracePeriod: usefulnessGracePeriod,

		df: df,
	}

	// 使用 context.WithCancel 函数创建一个后台上下文 ctx 和相应的取消函数 ctxCancel。
	rt.ctx, rt.ctxCancel = context.WithCancel(context.Background())

	return rt, nil
}

// Close 关闭路由表及其所有关联的进程。
// 可以安全地多次调用此函数。
func (rt *RoutingTable) Close() error {
	rt.ctxCancel()
	return nil
}

// NPeersForCpl 返回给定 Cpl 的对等节点数量。
func (rt *RoutingTable) NPeersForCpl(cpl uint) int {
	rt.tabLock.RLock()
	defer rt.tabLock.RUnlock()

	// 如果 Cpl 大于等于最后一个桶的索引
	if int(cpl) >= len(rt.buckets)-1 {
		count := 0
		b := rt.buckets[len(rt.buckets)-1]
		for _, p := range b.peers() {
			// 如果本地对等节点和当前对等节点的 DHT ID 的公共前缀长度等于 Cpl
			if CommonPrefixLen(rt.local, p.dhtId) == int(cpl) {
				count++
			}
		}
		return count
	} else {
		// 返回索引为 Cpl 的桶中的对等节点数量
		return rt.buckets[cpl].len()
	}
}

// UsefulNewPeer 验证给定的 peer.ID 是否适合路由表。
// 如果对等节点尚未在路由表中，或者与 peer.ID 对应的桶没有满，或者它包含可替换的对等节点，或者它是最后一个桶且添加对等节点会拆分该桶，则返回 true。
func (rt *RoutingTable) UsefulNewPeer(p peer.ID) bool {
	rt.tabLock.RLock()
	defer rt.tabLock.RUnlock()

	// 与 p 对应的桶
	bucketID := rt.bucketIdForPeer(p)
	bucket := rt.buckets[bucketID]

	if bucket.getPeer(p) != nil {
		// 对等节点已经存在于路由表中，因此不是有用的
		return false
	}

	// 桶未满
	if bucket.len() < rt.bucketsize {
		return true
	}

	// 桶已满，检查是否包含可替换的对等节点
	for e := bucket.list.Front(); e != nil; e = e.Next() {
		peer := e.Value.(*PeerInfo)
		if peer.replaceable {
			// 至少有一个可替换的对等节点
			return true
		}
	}

	// 最后一个桶可能包含具有不同 CPL 的对等节点 ID，并且如果需要，可以拆分为两个桶
	if bucketID == len(rt.buckets)-1 {
		peers := bucket.peers()
		cpl := CommonPrefixLen(rt.local, ConvertPeerID(p))
		for _, peer := range peers {
			// 如果至少有两个对等节点具有不同的 CPL，则新的对等节点是有用的，并且将触发桶的拆分
			if CommonPrefixLen(rt.local, peer.dhtId) != cpl {
				return true
			}
		}
	}

	// 适当的桶已满，且没有可替换的对等节点
	return false
}

// TryAddPeer 尝试将对等节点添加到路由表。
// 如果对等节点已经存在于路由表中并且之前已经查询过，则此调用不执行任何操作。
// 如果对等节点已经存在于路由表中但之前没有进行过查询，则将其 LastUsefulAt 值设置为当前时间。
// 这需要这样做是因为当我们第一次连接到对等节点时，我们不会将其标记为“有用”（通过设置 LastUsefulAt 值）。
//
// 如果对等节点是一个查询对等节点，即我们查询过它或它查询过我们，我们将 LastSuccessfulOutboundQuery 设置为当前时间。
// 如果对等节点只是一个我们连接到的对等节点/它连接到我们而没有进行任何 DHT 查询，则认为它没有 LastSuccessfulOutboundQuery。
//
// 如果对等节点所属的逻辑桶已满且不是最后一个桶，我们尝试用新的对等节点替换该桶中上次成功的出站查询时间超过允许阈值的现有对等节点。
// 如果该桶中不存在这样的对等节点，则不将对等节点添加到路由表中，并返回错误 "ErrPeerRejectedNoCapacity"。

// TryAddPeer 返回一个布尔值，如果对等节点是新添加到路由表中的，则设置为 true；否则为 false。
// 它还返回在将对等节点添加到路由表时发生的任何错误。如果错误不为 nil，则布尔值始终为 false，即对等节点不会被添加到路由表中。
//
// 返回值为 false 且错误为 nil 表示对等节点已经存在于路由表中。
func (rt *RoutingTable) TryAddPeer(p peer.ID, mode int, queryPeer bool, isReplaceable bool) (bool, error) {
	rt.tabLock.Lock()
	defer rt.tabLock.Unlock()

	return rt.addPeer(p, mode, queryPeer, isReplaceable)
}

// addPeer 方法将对等节点添加到路由表。
//
// addPeer 方法接受四个参数：p 表示要添加的对等节点的 peer.ID，queryPeer 表示对等节点是否是查询对等节点，
// isReplaceable 表示对等节点是否可替换。该方法返回一个布尔值，表示对等节点是否是新添加到路由表中的，
// 以及在将对等节点添加到路由表时发生的任何错误。
//
// 注意：调用 addPeer 方法之前，需要确保路由表的写入操作已经加锁。
func (rt *RoutingTable) addPeer(p peer.ID, mode int, queryPeer bool, isReplaceable bool) (bool, error) {
	// 根据对等节点的 peer.ID 计算桶的 ID。
	bucketID := rt.bucketIdForPeer(p)
	// 获取对应桶的引用。
	bucket := rt.buckets[bucketID]

	// 获取当前时间。
	now := time.Now()
	var lastUsefulAt time.Time
	if queryPeer {
		lastUsefulAt = now
	}

	// 对等节点已经存在于路由表中。
	if peerInfo := bucket.getPeer(p); peerInfo != nil {
		// 如果我们在添加对等节点之后第一次查询它，让我们给它一个有用性提升。这只会发生一次。
		if peerInfo.LastUsefulAt.IsZero() && queryPeer {
			peerInfo.LastUsefulAt = lastUsefulAt
		}
		return false, nil
	}

	// 对等节点的延迟阈值不可接受。
	if rt.metrics.LatencyEWMA(p) > rt.maxLatency {
		// 连接不符合要求，跳过！
		return false, ErrPeerRejectedHighLatency
	}

	// 将对等节点添加到多样性过滤器中。
	// 如果我们无法在表中找到对等节点的位置，我们将在稍后从过滤器中将其删除。
	if rt.df != nil {
		if !rt.df.TryAdd(p) {
			return false, fmt.Errorf("peer rejected by the diversity filter")
		}
	}

	// 我们在桶中有足够的空间（无论是生成的桶还是分组的桶）。
	if bucket.len() < rt.bucketsize {
		// 创建新的对等节点信息并将其添加到桶的前面。
		bucket.pushFront(&PeerInfo{
			Id:                            p,                // 对等节点的 ID
			Mode:                          mode,             // 当前的运行模式
			LastUsefulAt:                  lastUsefulAt,     // 对等节点上次对我们有用的时间点（请参阅 DHT 文档以了解有用性的定义）
			LastSuccessfulOutboundQueryAt: now,              // 我们最后一次从对等节点获得成功的查询响应的时间点
			AddedAt:                       now,              // 将此对等节点添加到路由表的时间点
			dhtId:                         ConvertPeerID(p), // 对等节点在 DHT XOR keyspace 中的 ID
			replaceable:                   isReplaceable,    // 如果一个桶已满，此对等节点可以被替换以为新对等节点腾出空间
		})
		// 更新路由表的相关状态。
		rt.PeerAdded(p)
		return true, nil
	}

	if bucketID == len(rt.buckets)-1 {
		// 如果桶太大，并且这是最后一个桶（即通配符桶），则展开它。
		rt.nextBucket()
		// 表的结构已经改变，因此让我们重新检查对等节点是否现在有一个专用的桶。
		bucketID = rt.bucketIdForPeer(p)
		bucket = rt.buckets[bucketID]

		// 仅在拆分后的桶不会溢出时才将对等节点推入。
		if bucket.len() < rt.bucketsize {
			// 创建新的对等节点信息并将其添加到桶的前面。
			bucket.pushFront(&PeerInfo{
				Id:                            p,                // 对等节点的 ID
				Mode:                          mode,             // 当前的运行模式
				LastUsefulAt:                  lastUsefulAt,     // 对等节点上次对我们有用的时间点（请参阅 DHT 文档以了解有用性的定义）
				LastSuccessfulOutboundQueryAt: now,              // 我们最后一次从对等节点获得成功的查询响应的时间点
				AddedAt:                       now,              // 将此对等节点添加到路由表的时间点
				dhtId:                         ConvertPeerID(p), // 对等节点在 DHT XOR keyspace 中的 ID
				replaceable:                   isReplaceable,    // 如果一个桶已满，此对等节点可以被替换以为新对等节点腾出空间
			})
			// 更新路由表的相关状态。
			rt.PeerAdded(p)
			return true, nil
		}
	}

	// 对等节点所属的桶已满。让我们尝试在该桶中找到一个可替换的对等节点。
	// 在这里我们不需要稳定排序，因为无论替换哪个对等节点，都无关紧要，只要它是可替换的对等节点即可。
	replaceablePeer := bucket.min(func(p1 *PeerInfo, p2 *PeerInfo) bool {
		return p1.replaceable
	})

	if replaceablePeer != nil && replaceablePeer.replaceable {
		// 我们找到了一个可替换的对等节点，让我们用新的对等节点替换它。

		// 将新的对等节点添加到桶中。在删除可替换的对等节点之前需要这样做，
		// 因为如果桶的大小为 1，我们将删除唯一的对等节点，并删除桶。
		bucket.pushFront(&PeerInfo{
			Id:                            p,                // 对等节点的 ID
			Mode:                          mode,             // 当前的运行模式
			LastUsefulAt:                  lastUsefulAt,     // 对等节点上次对我们有用的时间点（请参阅 DHT 文档以了解有用性的定义）
			LastSuccessfulOutboundQueryAt: now,              // 我们最后一次从对等节点获得成功的查询响应的时间点
			AddedAt:                       now,              // 将此对等节点添加到路由表的时间点
			dhtId:                         ConvertPeerID(p), // 对等节点在 DHT XOR keyspace 中的 ID
			replaceable:                   isReplaceable,    // 如果一个桶已满，此对等节点可以被替换以为新对等节点腾出空间
		})
		// 更新路由表的相关状态。
		rt.PeerAdded(p)

		// 移除被替换的对等节点。
		rt.removePeer(replaceablePeer.Id)
		return true, nil
	}

	// 我们无法找到对等节点的位置，从过滤器中将其删除。
	if rt.df != nil {
		rt.df.Remove(p)
	}
	return false, ErrPeerRejectedNoCapacity
}

// MarkAllPeersIrreplaceable 将路由表中的所有对等节点标记为不可替换。
// 这意味着我们永远不会替换表中的现有对等节点以为新对等节点腾出空间。
// 但是，可以通过调用 `RemovePeer` API 来删除它们。
func (rt *RoutingTable) MarkAllPeersIrreplaceable() {
	rt.tabLock.Lock()         // 锁定路由表，确保并发安全。
	defer rt.tabLock.Unlock() // 在函数返回时解锁路由表。

	for i := range rt.buckets {
		b := rt.buckets[i]
		b.updateAllWith(func(p *PeerInfo) {
			p.replaceable = false // 将每个对等节点的可替换属性设置为 false，标记为不可替换。
		})
	}
}

// GetPeerInfos 返回我们在桶中存储的对等节点信息。
func (rt *RoutingTable) GetPeerInfos() []PeerInfo {
	rt.tabLock.RLock()         // 以读取模式锁定路由表，确保并发安全。
	defer rt.tabLock.RUnlock() // 在函数返回时解锁路由表。

	var pis []PeerInfo
	for _, b := range rt.buckets {
		pis = append(pis, b.peers()...) // 将每个桶中的对等节点信息追加到 pis 切片中。
	}
	return pis // 返回包含所有对等节点信息的切片。
}

// UpdateLastSuccessfulOutboundQueryAt 更新对等节点的 LastSuccessfulOutboundQueryAt 时间。
// 如果更新成功，则返回 true；否则返回 false。
func (rt *RoutingTable) UpdateLastSuccessfulOutboundQueryAt(p peer.ID, t time.Time) bool {
	rt.tabLock.Lock()         // 锁定路由表，确保并发安全。
	defer rt.tabLock.Unlock() // 在函数返回时解锁路由表。

	bucketID := rt.bucketIdForPeer(p) // 获取对等节点所在的桶 ID。
	bucket := rt.buckets[bucketID]    // 获取对应的桶。

	if pc := bucket.getPeer(p); pc != nil {
		pc.LastSuccessfulOutboundQueryAt = t // 更新对等节点的 LastSuccessfulOutboundQueryAt 时间。
		return true                          // 更新成功，返回 true。
	}
	return false // 更新失败，返回 false。
}

// UpdateLastUsefulAt 更新对等节点的 LastUsefulAt 时间。
// 如果更新成功，则返回 true；否则返回 false。
func (rt *RoutingTable) UpdateLastUsefulAt(p peer.ID, t time.Time) bool {
	rt.tabLock.Lock()         // 锁定路由表，确保并发安全。
	defer rt.tabLock.Unlock() // 在函数返回时解锁路由表。

	bucketID := rt.bucketIdForPeer(p) // 获取对等节点所在的桶 ID。
	bucket := rt.buckets[bucketID]    // 获取对应的桶。

	if pc := bucket.getPeer(p); pc != nil {
		pc.LastUsefulAt = t // 更新对等节点的 LastUsefulAt 时间。
		return true         // 更新成功，返回 true。
	}
	return false // 更新失败，返回 false。
}

// RemovePeer 在调用者确定某个对等节点对查询不再有用时应调用此方法。
// 例如：对等节点可能停止支持 DHT 协议。
// 它从路由表中驱逐该对等节点。
func (rt *RoutingTable) RemovePeer(p peer.ID) {
	rt.tabLock.Lock()         // 锁定路由表，确保并发安全。
	defer rt.tabLock.Unlock() // 在函数返回时解锁路由表。
	rt.removePeer(p)          // 调用内部的 removePeer 方法，从路由表中移除对等节点。
}

// removePeer locking is the responsibility of the caller
// 锁定的责任由调用者承担
func (rt *RoutingTable) removePeer(p peer.ID) bool {
	bucketID := rt.bucketIdForPeer(p) // 获取对等节点所在的桶 ID。
	bucket := rt.buckets[bucketID]    // 获取对应的桶。

	if bucket.remove(p) { // 如果从桶中成功移除对等节点。
		if rt.df != nil {
			rt.df.Remove(p) // 如果存在默认路由函数，则从默认路由函数中移除对等节点。
		}

		for {
			lastBucketIndex := len(rt.buckets) - 1

			// 如果最后一个桶为空且不是唯一的桶，则移除最后一个桶。
			if len(rt.buckets) > 1 && rt.buckets[lastBucketIndex].len() == 0 {
				rt.buckets[lastBucketIndex] = nil
				rt.buckets = rt.buckets[:lastBucketIndex]
			} else if len(rt.buckets) >= 2 && rt.buckets[lastBucketIndex-1].len() == 0 {
				// 如果倒数第二个桶刚刚变为空，并且至少有两个桶，则移除倒数第二个桶，并用最后一个桶替换它。
				rt.buckets[lastBucketIndex-1] = rt.buckets[lastBucketIndex]
				rt.buckets[lastBucketIndex] = nil
				rt.buckets = rt.buckets[:lastBucketIndex]
			} else {
				break
			}
		}

		rt.PeerRemoved(p) // 对等节点移除回调函数。
		return true       // 移除成功，返回 true。
	}
	return false // 移除失败，返回 false。
}

// nextBucket 是展开路由表中的下一个桶
func (rt *RoutingTable) nextBucket() {
	// 这是最后一个桶，据说它是一个混合的容器，包含那些不属于专用（展开的）桶的节点。
	// 这里使用 "_allegedly_" 来表示最后一个桶中的 *所有* 节点可能实际上属于其他桶。
	// 这可能发生在我们展开了4个桶之后，最后一个桶中的所有节点实际上属于第8个桶。
	bucket := rt.buckets[len(rt.buckets)-1]

	// 将最后一个桶分割成两个桶，第一个桶保留原来的节点，第二个桶包含一部分节点。
	newBucket := bucket.split(len(rt.buckets)-1, rt.local)
	rt.buckets = append(rt.buckets, newBucket)

	// 新形成的桶仍然包含太多的节点。可能是因为我们展开了一个空的桶。
	if newBucket.len() >= rt.bucketsize {
		// 持续展开路由表，直到最后一个桶不再溢出。
		rt.nextBucket()
	}
}

// Find 根据给定的 ID 查找特定的节点，如果找不到则返回 nil
func (rt *RoutingTable) Find(id peer.ID) peer.ID {
	// 调用 NearestPeers 方法查找离给定 ID 最近的节点，最多返回一个节点
	srch := rt.NearestPeers(ConvertPeerID(id), 1)

	// 如果找不到节点或者找到的节点与给定的 ID 不匹配，则返回空字符串
	if len(srch) == 0 || srch[0] != id {
		return ""
	}

	// 返回找到的节点 ID
	return srch[0]
}

// NearestPeer  返回距离给定 ID 最近的单个节点
func (rt *RoutingTable) NearestPeer(id ID) peer.ID {
	// 调用 NearestPeers 方法查找距离给定 ID 最近的节点，最多返回一个节点
	peers := rt.NearestPeers(id, 1)

	// 如果找到了节点，则返回第一个节点的 ID
	if len(peers) > 0 {
		return peers[0]
	}

	// 打印调试信息，表示没有找到最近的节点，同时输出当前路由表的大小
	logrus.Debugf("NearestPeer: 返回空值，表的大小为 %d", rt.Size())

	// 返回空字符串表示没有找到最近的节点
	return ""
}

// NearestPeers 返回距离给定 ID 最近的 'count' 个节点的列表
func (rt *RoutingTable) NearestPeers(id ID, count int, mode ...int) []peer.ID {
	// 这是我们与目标键共享的位数。该桶中的所有节点与我们共享 cpl 位，因此它们与给定键至少共享 cpl+1 位。
	// +1 是因为目标和该桶中的所有节点在 cpl 位上与我们不同。
	cpl := CommonPrefixLen(id, rt.local)

	// 假设这也保护了桶。
	rt.tabLock.RLock()

	// 获取桶索引或最后一个桶
	if cpl >= len(rt.buckets) {
		cpl = len(rt.buckets) - 1
	}

	pds := peerDistanceSorter{
		peers:  make([]peerDistance, 0, count+rt.bucketsize),
		target: id,
	}

	// 从目标桶（cpl+1 个共享位）添加节点。
	pds.appendPeersFromList(rt.buckets[cpl].list)

	// 如果数量不足，从所有右侧的桶中添加节点。所有右侧的桶共享 cpl 位（与 cpl 桶中的节点共享 cpl+1 位不同）。
	//
	// 不幸的是，这比我们希望的要低效。我们最终将切换到 Trie 实现，它将允许我们找到任何目标键的最近 N 个节点。
	if pds.Len() < count {
		for i := cpl + 1; i < len(rt.buckets); i++ {
			pds.appendPeersFromList(rt.buckets[i].list)
		}
	}

	// 如果数量仍然不足，添加共享较少位数的桶中的节点。我们可以逐个桶地进行，因为每个桶与上一个桶相比共享的位数少 1。
	//
	// * 桶 cpl-1：cpl-1 个共享位。
	// * 桶 cpl-2：cpl-2 个共享位。
	// ...
	for i := cpl - 1; i >= 0 && pds.Len() < count; i-- {
		pds.appendPeersFromList(rt.buckets[i].list)
	}
	rt.tabLock.RUnlock()

	// 按与本地节点的距离排序
	pds.sort()

	// 如果数量小于 pds 的长度，则截取前 count 个节点
	if count < pds.Len() {
		pds.peers = pds.peers[:count]
	}

	// 构建输出列表
	out := make([]peer.ID, 0, pds.Len())
	for _, p := range pds.peers {
		// 没有提供可选值
		if len(mode) == 0 {
			out = append(out, p.p)
			continue
		}
		// 约束目标节点的运行模式
		if p.mode == mode[0] {
			out = append(out, p.p)
		}
	}

	return out
}

// Size 返回路由表中的节点总数
func (rt *RoutingTable) Size() int {
	var tot int
	rt.tabLock.RLock()

	// 遍历所有桶，累加每个桶中的节点数量
	for _, buck := range rt.buckets {
		tot += buck.len()
	}
	rt.tabLock.RUnlock()

	// 返回节点总数
	return tot
}

// ListPeers 从 RoutingTable 中获取所有桶中的所有节点，并返回节点列表
func (rt *RoutingTable) ListPeers() []peer.ID {
	rt.tabLock.RLock()
	defer rt.tabLock.RUnlock()

	var peers []peer.ID

	// 遍历所有桶，获取每个桶中的节点列表，并将其添加到 peers 列表中
	for _, buck := range rt.buckets {
		peers = append(peers, buck.peerIds()...)
	}

	// 返回节点列表
	return peers
}

// Print 打印关于提供的 RoutingTable 的描述性语句
func (rt *RoutingTable) Print() {
	fmt.Printf("Routing Table, bs = %d, Max latency = %d\n", rt.bucketsize, rt.maxLatency)
	rt.tabLock.RLock()

	// 遍历所有桶
	for i, b := range rt.buckets {
		fmt.Printf("\tbucket: %d\n", i)

		// 遍历当前桶中的所有节点
		for e := b.list.Front(); e != nil; e = e.Next() {
			p := e.Value.(*PeerInfo).Id
			m := e.Value.(*PeerInfo).Mode

			// 打印节点 ID 和节点的延迟信息
			fmt.Printf("\t\t- %s %d %s\n", p.String(), m, rt.metrics.LatencyEWMA(p).String())
		}
	}
	rt.tabLock.RUnlock()
}

// GetDiversityStats 如果配置了多样性过滤器，GetDiversityStats() 返回路由表的多样性统计信息
func (rt *RoutingTable) GetDiversityStats() []peerdiversity.CplDiversityStats {
	if rt.df != nil {
		return rt.df.GetDiversityStats()
	}
	return nil
}

// bucketIdForPeer 调用者负责加锁
func (rt *RoutingTable) bucketIdForPeer(p peer.ID) int {
	peerID := ConvertPeerID(p)
	cpl := CommonPrefixLen(peerID, rt.local)
	bucketID := cpl

	// 如果 bucketID 超出桶的范围，则将其设置为最后一个桶的 ID
	if bucketID >= len(rt.buckets) {
		bucketID = len(rt.buckets) - 1
	}

	return bucketID
}

// maxCommonPrefix 返回路由表中任意节点与当前节点之间的最大公共前缀长度
func (rt *RoutingTable) maxCommonPrefix() uint {
	rt.tabLock.RLock()
	defer rt.tabLock.RUnlock()

	// 从最后一个桶开始向前遍历
	for i := len(rt.buckets) - 1; i >= 0; i-- {
		if rt.buckets[i].len() > 0 {

			// 如果当前桶中有节点，则返回当前节点与本地节点的最大公共前缀长度
			return rt.buckets[i].maxCommonPrefix(rt.local)
		}
	}

	// 如果路由表为空，则返回 0
	return 0
}
