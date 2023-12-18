// kad-dht

package dep2p

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/bpfs/dep2p/internal"
	"github.com/bpfs/dep2p/qpeerset"

	kb "github.com/bpfs/dep2p/kbucket"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// queryFn 是执行查询的函数类型，用于查询给定对等节点的地址信息
type queryFn func(context.Context, peer.ID) ([]*peer.AddrInfo, error)

// stopFn 是用于确定是否应停止整个不连续查询的函数类型
type stopFn func(*qpeerset.QueryPeerset) bool

// query 表示单个 DHT 查询
type query struct {
	// lookup 实例的唯一标识符
	id uuid.UUID

	// 查询的目标键
	key string

	// 查询的上下文
	ctx context.Context

	dht *DeP2PDHT

	// seedPeers 是种子对等节点的集合，用于启动查询
	seedPeers []peer.ID

	// peerTimes 包含每个成功查询对等节点的持续时间
	peerTimes map[peer.ID]time.Duration

	// queryPeers 是此查询已知的对等节点集合及其各自的状态
	queryPeers *qpeerset.QueryPeerset

	// terminated 在第一个工作线程遇到终止条件时设置
	// 它的作用是确保一旦确定终止条件，就会保持终止状态
	terminated bool

	// waitGroup 确保查询在所有查询协程完成之前不会结束
	waitGroup sync.WaitGroup

	// 用于查询单个对等节点的函数
	queryFn queryFn

	// stopFn 用于确定是否应停止整个不连续查询
	stopFn stopFn
}

// lookupWithFollowupResult 包含查询结束时的前 K 个不可达对等节点、查询结束时的对等节点状态和前 K 个对等节点。
type lookupWithFollowupResult struct {
	peers   []peer.ID            // 查询结束时的前 K 个不可达对等节点
	state   []qpeerset.PeerState // 查询结束时的对等节点状态（不包括最接近的对等节点）
	closest []peer.ID            // 查询结束时的前 K 个对等节点

	// completed 表示查询和后续操作均未被外部条件（如上下文取消或停止函数调用）提前终止。
	completed bool
}

// runLookupWithFollowup 使用给定的查询函数在目标上执行查找，并在上下文被取消或停止函数返回 true 时停止。
// 注意：如果 stop 函数不是粘性的，即它不是在第一次返回 true 后每次都返回 true，则不能保证仅仅因为它暂时返回 true 就会导致停止发生。
//
// 查找完成后，查询函数将针对查找中尚未成功查询的所有前 K 个对等点运行（除非停止）。
func (dht *DeP2PDHT) runLookupWithFollowup(ctx context.Context, target string, queryFn queryFn, stopFn stopFn) (*lookupWithFollowupResult, error) {
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.RunLookupWithFollowup", trace.WithAttributes(internal.KeyAsAttribute("Target", target)))
	defer span.End()

	// 执行查询
	lookupRes, qps, err := dht.runQuery(ctx, target, queryFn, stopFn)
	if err != nil {
		return nil, err
	}

	// 查询我们听说过或正在等待的所有前 K 个对等点。
	// 这确保了所有前 K 个结果都已被查询，这增加了携带状态的查询函数（例如 FindProviders 和 GetValue）的抗流失能力，并建立无状态查询函数（例如 GetClosestPeers 和 Provide）所需的连接 和看跌期权价值）
	queryPeers := make([]peer.ID, 0, len(lookupRes.peers))
	for i, p := range lookupRes.peers {
		if state := lookupRes.state[i]; state == qpeerset.PeerHeard || state == qpeerset.PeerWaiting {
			queryPeers = append(queryPeers, p)
		}
	}

	if len(queryPeers) == 0 {
		return lookupRes, nil
	}

	// 如果查询已被外部停止，则返回
	if ctx.Err() != nil || stopFn(qps) {
		lookupRes.completed = false
		return lookupRes, nil
	}

	// 执行后续查询操作，并在完成时向 `doneCh` 通道发送信号。
	doneCh := make(chan struct{}, len(queryPeers))

	// 创建用于后续查询的上下文 `followUpCtx`，以及取消函数 `cancelFollowUp`。
	followUpCtx, cancelFollowUp := context.WithCancel(ctx)
	defer cancelFollowUp()

	// 遍历 `queryPeers` 切片中的每个元素执行后续查询操作。
	for _, p := range queryPeers {
		// 将当前迭代的 `p` 赋值给新的变量 `qp`，以便在闭包中使用。
		qp := p
		go func() {
			// 在后台执行查询函数，并传入 `followUpCtx` 上下文和 `qp` 参数。
			_, _ = queryFn(followUpCtx, qp)
			// 查询完成后，向 `doneCh` 通道发送信号。
			doneCh <- struct{}{}
		}()
	}

	// 等待所有查询完成后返回，如果我们被外部停止，则中止正在进行的查询
	followupsCompleted := 0
processFollowUp:
	// 等待所有后续查询操作完成，并根据不同情况更新 `lookupRes.completed` 的值。
	for i := 0; i < len(queryPeers); i++ {
		select {
		case <-doneCh:
			// 当接收到 `doneCh` 的消息时，表示一个后续查询操作已完成。
			followupsCompleted++
			if stopFn(qps) {
				// 如果满足停止条件，调用 `cancelFollowUp()` 取消后续查询。
				cancelFollowUp()
				if i < len(queryPeers)-1 {
					// 如果当前循环索引 `i` 小于 `queryPeers` 的长度减一，将 `lookupRes.completed` 设置为 false。
					lookupRes.completed = false
				}
				// 跳出 `processFollowUp` 标签所在的循环。
				break processFollowUp
			}
		case <-ctx.Done():
			// 当接收到 `ctx.Done()` 的消息时，表示上下文已被取消。
			lookupRes.completed = false
			// 调用 `cancelFollowUp()` 取消后续查询。
			cancelFollowUp()
			// 跳出 `processFollowUp` 标签所在的循环。
			break processFollowUp
		}
	}

	// 如果 `lookupRes.completed` 为 false，则等待剩余的查询完成。
	if !lookupRes.completed {
		for i := followupsCompleted; i < len(queryPeers); i++ {
			<-doneCh
		}
	}

	return lookupRes, nil
}

// 在 DeP2PDHT 中执行查询操作，并返回查询结果、查询节点集合和错误信息。
func (dht *DeP2PDHT) runQuery(ctx context.Context, target string, queryFn queryFn, stopFn stopFn) (*lookupWithFollowupResult, *qpeerset.QueryPeerset, error) {
	// 使用给定的上下文 `ctx` 和操作名称 "DeP2PDHT.RunQuery" 创建一个新的跟踪 span。
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.RunQuery")
	defer span.End()

	// 从目标键中获取对应的 Kademlia ID。
	targetKadID := kb.ConvertKey(target)

	// 从路由表中选择与目标键最接近的 K 个节点作为种子节点。
	seedPeers := dht.routingTable.NearestPeers(targetKadID, dht.bucketSize)

	// 如果没有找到种子节点，则发布查询错误事件并返回相应的错误信息。
	if len(seedPeers) == 0 {
		routing.PublishQueryEvent(ctx, &routing.QueryEvent{
			Type:  routing.QueryError,
			Extra: kb.ErrLookupFailure.Error(),
		})
		return nil, nil, kb.ErrLookupFailure
	}

	// 创建一个新的查询对象 `q`，并初始化其属性。
	q := &query{
		id:         uuid.New(),
		key:        target,
		ctx:        ctx,
		dht:        dht,
		queryPeers: qpeerset.NewQueryPeerset(target),
		seedPeers:  seedPeers,
		peerTimes:  make(map[peer.ID]time.Duration),
		terminated: false,
		queryFn:    queryFn,
		stopFn:     stopFn,
	}

	// 执行查询操作。
	q.run()

	// 如果上下文没有发生错误，则记录有价值的节点。
	if ctx.Err() == nil {
		q.recordValuablePeers()
	}

	// 构造查询结果 `res` 并返回。
	res := q.constructLookupResult(targetKadID)
	return res, q.queryPeers, nil
}

// queryUpdate 结构体用于存储查询更新的信息。
type queryUpdate struct {
	cause       peer.ID   // 查询的起因节点。
	queried     []peer.ID // 已经查询过的节点列表。
	heard       []peer.ID // 已经接收到回应的节点列表。
	unreachable []peer.ID // 无法到达的节点列表。

	queryDuration time.Duration // 查询的持续时间。
}

// run 是查询的执行方法。
func (q *query) run() {
	ctx, span := internal.StartSpan(q.ctx, "DeP2PDHT.Query.Run")
	defer span.End()

	pathCtx, cancelPath := context.WithCancel(ctx)
	defer cancelPath()

	alpha := q.dht.alpha

	ch := make(chan *queryUpdate, alpha)
	ch <- &queryUpdate{cause: q.dht.self, heard: q.seedPeers}

	// 等待所有未完成的查询完成后再返回。
	defer q.waitGroup.Wait()
	for {
		var cause peer.ID
		select {
		case update := <-ch:
			q.updateState(pathCtx, update)
			cause = update.cause
		case <-pathCtx.Done():
			q.terminate(pathCtx, cancelPath, LookupCancelled)
		}

		// 计算我们可以同时发起的最大查询数量。
		// 注意：NumWaiting 将在 spawnQuery 中进行更新。
		maxNumQueriesToSpawn := alpha - q.queryPeers.NumWaiting()

		// 根据查找结束条件或未使用节点的饥饿情况触发终止。
		// 同时返回我们应该查询的下一组节点，最多不超过 `maxNumQueriesToSpawn` 个节点。
		ready, reason, qPeers := q.isReadyToTerminate(pathCtx, maxNumQueriesToSpawn)
		if ready {
			q.terminate(pathCtx, cancelPath, reason)
		}

		if q.terminated {
			return
		}

		// 尝试生成查询，如果没有可用的节点可供查询，则不会生成查询。
		for _, p := range qPeers {
			q.spawnQuery(pathCtx, cause, p, ch)
		}
	}
}

// 记录有价值的节点。
func (q *query) recordValuablePeers() {
	// 有价值节点的算法：
	// 将响应查询的时间最短的种子节点标记为 "最有价值节点"（MVP）
	// 每个在 MVP 时间范围内（例如，2 倍）响应查询的种子节点都是有价值的节点
	// 将 MVP 和所有其他有价值的节点标记为有价值节点
	mvpDuration := time.Duration(math.MaxInt64)

	// 找到响应时间最短的种子节点作为 MVP。
	for _, p := range q.seedPeers {
		if queryTime, ok := q.peerTimes[p]; ok && queryTime < mvpDuration {
			mvpDuration = queryTime
		}
	}

	// 将在 MVP 时间范围内响应查询的种子节点都标记为有价值节点。
	for _, p := range q.seedPeers {
		if queryTime, ok := q.peerTimes[p]; ok && queryTime < mvpDuration*2 {
			q.recordPeerIsValuable(p)
		}
	}
}

// constructLookupResult 使用查询信息构建查找结果。
func (q *query) constructLookupResult(target kb.ID) *lookupWithFollowupResult {
	// 确定查询是否提前终止。
	completed := true

	// 查找终止和饥饿终止都是查询完成的有效方式（饥饿终止并不意味着失败）。
	// 在小型网络中，不可能发生查找终止（根据 isLookupTermination 的定义）。
	// 饥饿终止是小型网络中的成功查询终止。
	if !(q.isLookupTermination() || q.isStarvationTermination()) {
		completed = false
	}

	// 提取非不可达节点的前 K 个节点。
	var peers []peer.ID
	peerState := make(map[peer.ID]qpeerset.PeerState)
	qp := q.queryPeers.GetClosestNInStates(q.dht.bucketSize, qpeerset.PeerHeard, qpeerset.PeerWaiting, qpeerset.PeerQueried)
	for _, p := range qp {
		state := q.queryPeers.GetState(p)
		peerState[p] = state
		peers = append(peers, p)
	}

	// 获取整体前 K 个节点。
	sortedPeers := kb.SortClosestPeers(peers, target)
	if len(sortedPeers) > q.dht.bucketSize {
		sortedPeers = sortedPeers[:q.dht.bucketSize]
	}

	// 获取不可达节点的前 K 个节点。
	closest := q.queryPeers.GetClosestNInStates(q.dht.bucketSize, qpeerset.PeerHeard, qpeerset.PeerWaiting, qpeerset.PeerQueried, qpeerset.PeerUnreachable)

	// 返回非不可达节点的前 K 个节点以及查询结束时它们的状态。
	res := &lookupWithFollowupResult{
		peers:     sortedPeers,
		state:     make([]qpeerset.PeerState, len(sortedPeers)),
		completed: completed,
		closest:   closest,
	}

	for i, p := range sortedPeers {
		res.state[i] = peerState[p]
	}

	return res
}

// updateState 方法用于更新查询状态并发布查询事件。
func (q *query) updateState(ctx context.Context, up *queryUpdate) {
	if q.terminated {
		panic("update should not be invoked after the logical lookup termination")
	}

	// 发布查询事件
	PublishLookupEvent(ctx,
		NewLookupEvent(
			q.dht.self,
			q.id,
			q.key,
			nil,
			NewLookupUpdateEvent(
				up.cause,
				up.cause,
				up.heard,       // 已听说的节点
				nil,            // 等待中的节点
				up.queried,     // 已查询的节点
				up.unreachable, // 不可达的节点
			),
			nil,
		),
	)

	// 处理已听说的节点
	for _, p := range up.heard {
		if p == q.dht.self { // 不要添加自身节点
			continue
		}
		// 尝试将节点添加到查询节点集合中
		q.queryPeers.TryAdd(p, up.cause)
	}

	// 处理已查询的节点
	for _, p := range up.queried {
		if p == q.dht.self { // 不要添加自身节点
			continue
		}
		// 检查节点的状态
		if st := q.queryPeers.GetState(p); st == qpeerset.PeerWaiting {
			// 将节点状态设置为已查询，并记录查询持续时间
			q.queryPeers.SetState(p, qpeerset.PeerQueried)
			q.peerTimes[p] = up.queryDuration
		} else {
			panic(fmt.Errorf("kademlia protocol error: tried to transition to the queried state from state %v", st))
		}
	}
	// 处理不可达的节点
	for _, p := range up.unreachable {
		if p == q.dht.self { // 不要添加自身节点
			continue
		}

		// 检查节点的状态
		if st := q.queryPeers.GetState(p); st == qpeerset.PeerWaiting {
			// 将节点状态设置为不可达
			q.queryPeers.SetState(p, qpeerset.PeerUnreachable)
		} else {
			panic(fmt.Errorf("kademlia protocol error: tried to transition to the unreachable state from state %v", st))
		}
	}
}

// terminate 方法用于终止查询，并发布查询终止事件。
func (q *query) terminate(ctx context.Context, cancel context.CancelFunc, reason LookupTerminationReason) {
	// 创建一个新的上下文和跟踪 span
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.Query.Terminate", trace.WithAttributes(attribute.Stringer("Reason", reason)))
	defer span.End()

	// 如果已经终止过查询，则直接返回
	if q.terminated {
		return
	}

	// 发布查询终止事件
	PublishLookupEvent(ctx,
		NewLookupEvent(
			q.dht.self,
			q.id,
			q.key,
			nil,
			nil,
			NewLookupTerminateEvent(reason),
		),
	)
	// 取消未完成的查询
	cancel()
	// 标记查询已终止
	q.terminated = true
}

// isReadyToTerminate 方法用于判断查询是否准备终止，并返回终止的原因和待查询的节点列表。
func (q *query) isReadyToTerminate(ctx context.Context, nPeersToQuery int) (bool, LookupTerminationReason, []peer.ID) {
	// 给应用逻辑一个机会来终止查询
	if q.stopFn(q.queryPeers) {
		return true, LookupStopped, nil
	}
	if q.isStarvationTermination() {
		return true, LookupStarvation, nil
	}
	if q.isLookupTermination() {
		return true, LookupCompleted, nil
	}

	// 下一步查询的节点应为我们仅仅听说过的节点。
	var peersToQuery []peer.ID
	// 获取仅仅听说过的节点列表
	peers := q.queryPeers.GetClosestInStates(qpeerset.PeerHeard)
	count := 0
	// 遍历节点列表
	for _, p := range peers {
		// 将节点添加到待查询的节点列表中
		peersToQuery = append(peersToQuery, p)
		count++
		// 如果已选择的节点数达到指定的待查询节点数，则跳出循环
		if count == nPeersToQuery {
			break
		}
	}

	// 返回终止标志为假、终止原因为 -1（未定义）以及待查询的节点列表
	return false, -1, peersToQuery
}

// spawnQuery 方法在找到一个可用的已接收回应的节点时启动一个查询。
func (q *query) spawnQuery(ctx context.Context, cause peer.ID, queryPeer peer.ID, ch chan<- *queryUpdate) {
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.SpawnQuery", trace.WithAttributes(
		attribute.String("Cause", cause.String()),
		attribute.String("QueryPeer", queryPeer.String()),
	))
	defer span.End()

	// 发布查询事件，记录查询的相关信息。
	PublishLookupEvent(ctx,
		NewLookupEvent(
			q.dht.self,
			q.id,
			q.key,
			NewLookupUpdateEvent(
				cause,
				q.queryPeers.GetReferrer(queryPeer),
				nil,                  // 已接收回应的节点列表
				[]peer.ID{queryPeer}, // 等待的节点列表
				nil,                  // 已查询的节点列表
				nil,                  // 无法到达的节点列表
			),
			nil,
			nil,
		),
	)

	// 将查询的目标节点的状态设置为等待状态。
	q.queryPeers.SetState(queryPeer, qpeerset.PeerWaiting)
	q.waitGroup.Add(1)
	go q.queryPeer(ctx, ch, queryPeer)
}

// 记录节点 `p` 是有价值的。
func (q *query) recordPeerIsValuable(p peer.ID) {
	// 更新节点 `p` 在路由表中的最后有用时间戳为当前时间。
	if !q.dht.routingTable.UpdateLastUsefulAt(p, time.Now()) {
		// 如果节点 `p` 不在路由表中，则返回。
		return
	}
}

// isLookupTermination 方法用于判断查询是否满足终止条件。
// 如果所有非不可达节点中最接近的 beta 个节点都已查询过，则查询可以终止。
func (q *query) isLookupTermination() bool {
	// 从所有非不可达节点中获取最接近的 beta 个节点
	peers := q.queryPeers.GetClosestNInStates(q.dht.beta, qpeerset.PeerHeard, qpeerset.PeerWaiting, qpeerset.PeerQueried)
	for _, p := range peers {
		// 如果节点的状态不是已查询，则返回假
		if q.queryPeers.GetState(p) != qpeerset.PeerQueried {
			return false
		}
	}
	// 所有最接近的 beta 个节点都已查询过，返回真
	return true
}

// isStarvationTermination 方法用于判断查询是否出现饥饿终止的情况。
// 如果已听说的节点数为 0 并且等待的节点数为 0，则查询出现饥饿终止。
func (q *query) isStarvationTermination() bool {
	// 如果已听说的节点数为 0 并且等待的节点数为 0，则返回真
	return q.queryPeers.NumHeard() == 0 && q.queryPeers.NumWaiting() == 0
}

// queryPeer 方法用于查询单个节点，并通过通道报告查询结果。
// queryPeer 方法不会访问 queryPeers 中的查询状态！
func (q *query) queryPeer(ctx context.Context, ch chan<- *queryUpdate, p peer.ID) {
	defer q.waitGroup.Done()

	// 创建一个新的上下文和跟踪 span
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.QueryPeer")
	defer span.End()

	dialCtx, queryCtx := ctx, ctx

	// 拨号连接到节点
	if err := q.dht.dialPeer(dialCtx, p); err != nil {
		// 如果拨号失败，并且不是因为上下文取消，则移除该节点
		if dialCtx.Err() == nil {
			q.dht.peerStoppedDHT(p)
		}
		// 向通道发送查询更新，标记该节点为不可达
		ch <- &queryUpdate{cause: p, unreachable: []peer.ID{p}}
		return
	}

	startQuery := time.Now()
	// 向远程节点发送查询 RPC
	newPeers, err := q.queryFn(queryCtx, p)
	if err != nil {
		if queryCtx.Err() == nil {
			q.dht.peerStoppedDHT(p)
		}
		// 向通道发送查询更新，标记该节点为不可达
		ch <- &queryUpdate{cause: p, unreachable: []peer.ID{p}}
		return
	}

	queryDuration := time.Since(startQuery)

	// 查询成功，尝试将节点添加到路由表
	q.dht.validPeerFound(p)

	// 处理新的节点
	saw := []peer.ID{}
	for _, next := range newPeers {
		if next.ID == q.dht.self { // 不要添加自身节点
			logrus.Debugf("PEERS CLOSER -- worker for: %v found self", p)
			continue
		}

		// 添加候选节点的其他已知地址
		curInfo := q.dht.peerstore.PeerInfo(next.ID)
		next.Addrs = append(next.Addrs, curInfo.Addrs...)

		// 将节点的地址添加到拨号器的 peerstore 中
		//
		// 如果节点匹配查询目标或者在查询过滤器中通过，则将其添加到查询中
		// TODO: 这个行为实际上只适用于 FindPeer 的工作方式，而不适用于 GetClosestPeers 或其他函数
		isTarget := string(next.ID) == q.key
		if isTarget || q.dht.queryPeerFilter(q.dht, *next) {
			q.dht.maybeAddAddrs(next.ID, next.Addrs, pstore.TempAddrTTL)
			saw = append(saw, next.ID)
		}
	}

	// 向通道发送查询更新，包括已听说的节点、已查询的节点、查询持续时间等信息
	ch <- &queryUpdate{cause: p, heard: saw, queried: []peer.ID{p}, queryDuration: queryDuration}
}

// dialPeer 方法用于与指定的节点建立连接。
func (dht *DeP2PDHT) dialPeer(ctx context.Context, p peer.ID) error {
	ctx, span := internal.StartSpan(ctx, "DeP2PDHT.DialPeer", trace.WithAttributes(attribute.String("PeerID", p.String())))
	defer span.End()

	// 如果已经连接，则直接返回
	if dht.host.Network().Connectedness(p) == network.Connected {
		return nil
	}

	logrus.Debugf("not connected. dialing.")
	routing.PublishQueryEvent(ctx, &routing.QueryEvent{
		Type: routing.DialingPeer,
		ID:   p,
	})

	pi := peer.AddrInfo{ID: p}
	if err := dht.host.Connect(ctx, pi); err != nil {
		logrus.Debugf("error connecting: %s", err)
		routing.PublishQueryEvent(ctx, &routing.QueryEvent{
			Type:  routing.QueryError,
			Extra: err.Error(),
			ID:    p,
		})

		return err
	}
	logrus.Debugln("connected. dial success.")
	return nil
}
