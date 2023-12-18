// discovery
// https://github.com/libp2p/go-libp2p/tree/master/p2p/discovery/routing

package dep2p

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bpfs/dep2p/internal"
	"github.com/bpfs/dep2p/qpeerset"

	kb "github.com/bpfs/dep2p/kbucket"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// RoutingDiscovery is an implementation of discovery using ContentRouting.
// Namespaces are translated to Cids using the SHA256 hash.
type RoutingDiscovery struct {
	routing.ContentRouting
}

func NewRoutingDiscovery(router routing.ContentRouting) *RoutingDiscovery {
	return &RoutingDiscovery{router}
}

func (d *RoutingDiscovery) Advertise(ctx context.Context, ns string, opts ...discovery.Option) (time.Duration, error) {
	var options discovery.Options
	err := options.Apply(opts...)
	if err != nil {
		return 0, err
	}

	ttl := options.Ttl
	if ttl == 0 || ttl > 3*time.Hour {
		// the DHT provider record validity is 24hrs, but it is recommended to republish at least every 6hrs
		// we go one step further and republish every 3hrs
		ttl = 3 * time.Hour
	}

	cid, err := nsToCid(ns)
	if err != nil {
		return 0, err
	}

	// this context requires a timeout; it determines how long the DHT looks for
	// closest peers to the key/CID before it goes on to provide the record to them.
	// Not setting a timeout here will make the DHT wander forever.
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	err = d.Provide(pctx, cid, true)
	if err != nil {
		return 0, err
	}

	return ttl, nil
}

func (d *RoutingDiscovery) FindPeers(ctx context.Context, ns string, opts ...discovery.Option) (<-chan peer.AddrInfo, error) {
	var options discovery.Options
	err := options.Apply(opts...)
	if err != nil {
		return nil, err
	}

	limit := options.Limit
	if limit == 0 {
		limit = 100 // that's just arbitrary, but FindProvidersAsync needs a count
	}

	cid, err := nsToCid(ns)
	if err != nil {
		return nil, err
	}

	return d.FindProvidersAsync(ctx, cid, limit), nil
}

func nsToCid(ns string) (cid.Cid, error) {
	h, err := multihash.Sum([]byte(ns), multihash.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}

	return cid.NewCidV1(cid.Raw, h), nil
}

// func NewDiscoveryRouting(disc discovery.Discovery, opts ...discovery.Option) *DiscoveryRouting {
// 	return &DiscoveryRouting{disc, opts}
// }

// type DiscoveryRouting struct {
// 	discovery.Discovery
// 	opts []discovery.Option
// }

// func (r *DiscoveryRouting) Provide(ctx context.Context, c cid.Cid, bcast bool) error {
// 	if !bcast {
// 		return nil
// 	}

// 	_, err := r.Advertise(ctx, cidToNs(c), r.opts...)
// 	return err
// }

// func (r *DiscoveryRouting) FindProvidersAsync(ctx context.Context, c cid.Cid, limit int) <-chan peer.AddrInfo {
// 	ch, _ := r.FindPeers(ctx, cidToNs(c), append([]discovery.Option{discovery.Limit(limit)}, r.opts...)...)
// 	return ch
// }

// func cidToNs(c cid.Cid) string {
// 	return "/provider/" + c.String()
// }

////////////////////////

// ContentRouting 是一个值提供者的间接层。它用于找到谁拥有什么内容的信息。
//
// 内容由CID（内容标识符）标识，它以未来可证明的方式编码了被标识内容的哈希值。
type ContentRouting interface {
	// Provide 将给定的 cid 添加到内容路由系统中。如果传递了 'true'，
	// 它还会宣布它，否则它只是保留在本地的对象提供的记账中。
	Provide(context.Context, cid.Cid, bool) error

	// 搜索能够提供给定键的对等方
	//
	// 当计数为0时，此方法将返回无限制数量的结果。
	FindProvidersAsync(context.Context, cid.Cid, int) <-chan peer.AddrInfo
}

// 该文件实现了 DeP2PDHT 结构的 Routing 接口。

// 基本的 Put/Get 操作。

// Provider 是间接存储的提供者抽象。
// 一些 DHT 直接存储值，而间接存储则存储指向值位置的指针，类似于 Coral 和 Mainline DHT。

// Provide 使该节点宣布可以为给定的键提供值。
func (dht *DeP2PDHT) Provide(ctx context.Context, key cid.Cid, brdcst bool) (err error) {
	// 在上下文中启动一个跟踪 Span，并命名为 "DeP2PDHT.Provide"，并设置相关属性。
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.Provide", trace.WithAttributes(attribute.String("Key", key.String()), attribute.Bool("Broadcast", brdcst)))
	defer span.End()

	// 如果未启用提供者功能，则返回不支持的错误。
	if !dht.enableProviders {
		return routing.ErrNotSupported
	} else if !key.Defined() {
		return fmt.Errorf("无效的 CID：未定义")
	}
	keyMH := key.Hash()

	// logger.Debugw("providing", "cid", key, "mh", internal.LoggableProviderRecordBytes(keyMH))

	// 在本地添加自己作为提供者
	dht.providerStore.AddProvider(ctx, keyMH, peer.AddrInfo{ID: dht.self})
	if !brdcst {
		return nil
	}

	// 根据配置决定使用乐观提供还是经典提供，并处理可能的错误情况。
	// if dht.enableOptProv {
	// 	err := dht.optimisticProvide(ctx, keyMH)
	// 	if errors.Is(err, netsize.ErrNotEnoughData) {
	// 		logger.Debugln("数据不足，采用经典方法进行提供")
	// 		return dht.classicProvide(ctx, keyMH)
	// 	}
	// 	return err
	// }

	return dht.classicProvide(ctx, keyMH)
}

// FindProvidersAsync 与 FindProviders 方法相同，但返回一个通道。
// 对等节点将在找到时立即通过通道返回，即使搜索查询尚未完成。
// 如果 count 为零，则查询将一直运行，直到完成。
// 注意：不读取返回的通道可能会阻塞查询的进展。
func (dht *DeP2PDHT) FindProvidersAsync(ctx context.Context, key cid.Cid, count int) <-chan peer.AddrInfo {
	if !dht.enableProviders || !key.Defined() {
		// 如果未启用提供者功能或提供的 CID 未定义，则创建一个已关闭的对等节点输出通道，并返回该通道。
		peerOut := make(chan peer.AddrInfo)
		close(peerOut)
		return peerOut
	}

	chSize := count
	if count == 0 {
		// 如果 count 为零，则将通道大小设置为 1。
		chSize = 1
	}
	// 创建一个具有指定容量的对等节点输出通道。
	peerOut := make(chan peer.AddrInfo, chSize)

	// 计算提供者记录的哈希。
	keyMH := key.Hash()

	// 打印调试日志，记录正在查找提供者。
	// logger.Debugw("finding providers", "cid", key, "mh", internal.LoggableProviderRecordBytes(keyMH))

	// 在后台启动异步查找提供者的例程，并将结果发送到对等节点输出通道。
	go dht.findProvidersAsyncRoutine(ctx, keyMH, count, peerOut)
	return peerOut
}

// classicProvide 使用经典的方法向最接近的对等节点提供提供者记录，并处理超时和错误情况。
func (dht *DeP2PDHT) classicProvide(ctx context.Context, keyMH multihash.Multihash) error {
	// 复制上下文以便在需要时进行取消操作。
	closerCtx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		now := time.Now()
		timeout := deadline.Sub(now)

		if timeout < 0 {
			// 超时
			return context.DeadlineExceeded
		} else if timeout < 10*time.Second {
			// 保留10%的时间用于最后的放置操作。
			deadline = deadline.Add(-timeout / 10)
		} else {
			// 否则，保留一秒钟（因为我们已经连接上了，所以应该很快）。
			deadline = deadline.Add(-time.Second)
		}
		var cancel context.CancelFunc
		closerCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	var exceededDeadline bool
	peers, err := dht.GetClosestPeers(closerCtx, string(keyMH))
	switch err {
	case context.DeadlineExceeded:
		// 如果内部的截止时间已经超过，但外部的上下文仍然有效，
		// 则将值提供给我们找到的最接近的对等节点，即使它们不是实际的最接近对等节点。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		exceededDeadline = true
	case nil:
	default:
		return err
	}

	// 创建一个等待组。
	wg := sync.WaitGroup{}
	for _, p := range peers {
		// 增加等待组的计数器。
		wg.Add(1)
		go func(p peer.ID) {
			defer wg.Done()
			// 打印调试日志，记录提供者记录的放置操作。
			// logrus.Debugln("putProvider(%s, %s)", internal.LoggableProviderRecordBytes(keyMH), p)
			// 使用协议传递器向对等节点发送提供者记录。
			err := dht.protoMessenger.PutProvider(ctx, p, keyMH, dht.host)
			if err != nil {
				// 如果发生错误，打印错误日志。
				logrus.Debug(err)
			}
		}(p)
	}
	// 等待所有协程完成。
	wg.Wait()
	if exceededDeadline {
		// 如果在内部截止时间已过期但外部上下文仍然有效的情况下执行了提供操作，则返回 `context.DeadlineExceeded` 错误。
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

// findProvidersAsyncRoutine 是 FindProvidersAsync 方法的后台例程，用于异步查找提供者。
func (dht *DeP2PDHT) findProvidersAsyncRoutine(ctx context.Context, key multihash.Multihash, count int, peerOut chan peer.AddrInfo) {
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.FindProvidersAsyncRoutine", trace.WithAttributes(attribute.Stringer("Key", key)))
	defer span.End()

	defer close(peerOut)

	findAll := count == 0

	// 用于存储找到的对等节点信息的映射
	ps := make(map[peer.ID]peer.AddrInfo)
	psLock := &sync.Mutex{}
	psTryAdd := func(p peer.AddrInfo) bool {
		// 加锁以保证并发安全
		psLock.Lock()
		defer psLock.Unlock()
		pi, ok := ps[p.ID]
		// 如果对等节点不存在或者已存在的对等节点没有地址，而新对等节点有地址，并且对等节点数量未达到 count 或者需要找到所有提供者，则将新对等节点添加到映射中
		if (!ok || ((len(pi.Addrs) == 0) && len(p.Addrs) > 0)) && (len(ps) < count || findAll) {
			ps[p.ID] = p
			return true
		}
		return false
	}
	psSize := func() int {
		// 加锁以保证并发安全
		psLock.Lock()
		defer psLock.Unlock()
		return len(ps)
	}

	// 从提供者存储中获取提供者信息
	provs, err := dht.providerStore.GetProviders(ctx, key)
	if err != nil {
		return
	}
	for _, p := range provs {
		// 注意：假设对等节点列表是唯一的
		if psTryAdd(p) {
			select {
			case peerOut <- p:
				span.AddEvent("found provider", trace.WithAttributes(
					attribute.Stringer("peer", p.ID),
					attribute.Stringer("from", dht.self),
				))
			case <-ctx.Done():
				return
			}
		}

		// 如果本地已经有足够的对等节点，则不再进行远程 RPC
		// TODO: 这是否会导致拒绝服务攻击？
		if !findAll && len(ps) >= count {
			return
		}
	}

	lookupRes, err := dht.runLookupWithFollowup(ctx, string(key),
		func(ctx context.Context, p peer.ID) ([]*peer.AddrInfo, error) {

			// 对于 DHT 查询命令
			routing.PublishQueryEvent(ctx, &routing.QueryEvent{
				Type: routing.SendingQuery,
				ID:   p,
			})

			provs, closest, err := dht.protoMessenger.GetProviders(ctx, p, key)
			if err != nil {
				return nil, err
			}

			// 打印提供者条目数量
			logrus.Debugf("提供者条目数量：%d", len(provs))

			// 添加请求中的唯一提供者，最多添加 'count' 个
			for _, prov := range provs {
				// 将提供者的地址添加到本地对等节点存储中，设置临时地址过期时间
				dht.maybeAddAddrs(prov.ID, prov.Addrs, peerstore.TempAddrTTL)
				logrus.Debugf("获取到提供者：%s", prov)
				if psTryAdd(*prov) {
					logrus.Debugf("使用提供者：%s", prov)
					select {
					case peerOut <- *prov:
						// 记录跟踪事件：找到提供者
						span.AddEvent("找到提供者", trace.WithAttributes(
							attribute.Stringer("peer", prov.ID),
							attribute.Stringer("from", p),
						))
					case <-ctx.Done():
						logrus.Debug("上下文超时，无法发送更多提供者")
						return nil, ctx.Err()
					}
				}
				if !findAll && psSize() >= count {
					logrus.Debugf("已获取足够的提供者（%d/%d）", psSize(), count)
					return nil, nil
				}
			}

			// 输出日志：获取到更近的对等节点，打印对等节点数量和对等节点信息
			logrus.Debugf("获取到更近的对等节点：%d %s", len(closest), closest)

			// 发布查询事件，类型为 PeerResponse，包含查询的 ID 和响应的对等节点列表
			routing.PublishQueryEvent(ctx, &routing.QueryEvent{
				Type:      routing.PeerResponse,
				ID:        p,
				Responses: closest,
			})
			// 返回找到的更近的对等节点列表和 nil 错误
			return closest, nil
		},
		func(*qpeerset.QueryPeerset) bool {
			return !findAll && psSize() >= count
		},
	)

	if err == nil && ctx.Err() == nil {
		dht.refreshRTIfNoShortcut(kb.ConvertKey(string(key)), lookupRes)
	}
}

// refreshRTIfNoShortcut 如果没有找到捷径，刷新路由表。
// 它接收一个键 key 和一个 lookupRes 结果。
func (dht *DeP2PDHT) refreshRTIfNoShortcut(key kb.ID, lookupRes *lookupWithFollowupResult) {
	if lookupRes.completed {
		// 如果查询成功，刷新该键的 cpl（最长公共前缀长度）
		dht.routingTable.ResetCplRefreshedAtForID(key, time.Now())
	}
}
