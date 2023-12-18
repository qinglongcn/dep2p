package rtrefresh

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bpfs/dep2p/internal"

	kbucket "github.com/bpfs/dep2p/kbucket"

	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-base32"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	peerPingTimeout = 10 * time.Second // 对等节点 ping 的超时时间
)

type triggerRefreshReq struct {
	respCh          chan error // 刷新请求的响应通道
	forceCplRefresh bool       // 是否强制刷新指定 cpl
}

// RtRefreshManager 是一个用于管理 Routing Table 刷新的结构体。
// 它包含了执行刷新操作所需的上下文、计数器、本地对等节点信息、Routing Table、刷新相关的配置参数和函数、刷新触发请求的通道等。
type RtRefreshManager struct {
	ctx      context.Context
	cancel   context.CancelFunc
	refcount sync.WaitGroup

	// peerId of this DHT peer i.e. self peerId.
	h         host.Host
	dhtPeerId peer.ID
	rt        *kbucket.RoutingTable

	enableAutoRefresh   bool                                        // should run periodic refreshes ?
	refreshKeyGenFnc    func(cpl uint) (string, error)              // generate the key for the query to refresh this cpl
	refreshQueryFnc     func(ctx context.Context, key string) error // query to run for a refresh.
	refreshPingFnc      func(ctx context.Context, p peer.ID) error  // request to check liveness of remote peer
	refreshQueryTimeout time.Duration                               // timeout for one refresh query

	// interval between two periodic refreshes.
	// also, a cpl wont be refreshed if the time since it was last refreshed
	// is below the interval..unless a "forced" refresh is done.
	refreshInterval                    time.Duration
	successfulOutboundQueryGracePeriod time.Duration

	triggerRefresh chan *triggerRefreshReq // channel to write refresh requests to.

	refreshDoneCh chan struct{} // write to this channel after every refresh
}

func NewRtRefreshManager(h host.Host, rt *kbucket.RoutingTable, autoRefresh bool,
	refreshKeyGenFnc func(cpl uint) (string, error),
	refreshQueryFnc func(ctx context.Context, key string) error,
	refreshPingFnc func(ctx context.Context, p peer.ID) error,
	refreshQueryTimeout time.Duration,
	refreshInterval time.Duration,
	successfulOutboundQueryGracePeriod time.Duration,
	refreshDoneCh chan struct{}) (*RtRefreshManager, error) {

	ctx, cancel := context.WithCancel(context.Background())
	return &RtRefreshManager{
		ctx:       ctx,
		cancel:    cancel,
		h:         h,
		dhtPeerId: h.ID(),
		rt:        rt,

		enableAutoRefresh: autoRefresh,
		refreshKeyGenFnc:  refreshKeyGenFnc,
		refreshQueryFnc:   refreshQueryFnc,
		refreshPingFnc:    refreshPingFnc,

		refreshQueryTimeout:                refreshQueryTimeout,
		refreshInterval:                    refreshInterval,
		successfulOutboundQueryGracePeriod: successfulOutboundQueryGracePeriod,

		triggerRefresh: make(chan *triggerRefreshReq),
		refreshDoneCh:  refreshDoneCh,
	}, nil
}

// Start 启动 RtRefreshManager 实例的循环处理函数。
func (r *RtRefreshManager) Start() {
	r.refcount.Add(1)
	go r.loop()
}

// Close 关闭 RtRefreshManager 实例。
func (r *RtRefreshManager) Close() error {
	r.cancel()
	r.refcount.Wait()
	return nil
}

// RefreshRoutingTable 请求刷新管理器刷新路由表。
// 如果force参数设置为true true，则所有bucket都将被刷新，无论它们上次刷新是什么时候。
//
// 返回的通道将阻塞直到刷新完成，然后产生错误并关闭。 该通道已缓冲并且可以安全地忽略。
func (r *RtRefreshManager) Refresh(force bool) <-chan error {
	resp := make(chan error, 1)
	r.refcount.Add(1)
	go func() {
		defer r.refcount.Done()
		select {
		case r.triggerRefresh <- &triggerRefreshReq{respCh: resp, forceCplRefresh: force}:
		case <-r.ctx.Done():
			resp <- r.ctx.Err()
			close(resp)
		}
	}()

	return resp
}

// RefreshNoWait 请求刷新管理器刷新路由表。
// 但是，如果请求无法通过，它会继续前进而不会阻塞。
func (r *RtRefreshManager) RefreshNoWait() {
	select {
	case r.triggerRefresh <- &triggerRefreshReq{}:
	default:
	}
}

// pingAndEvictPeers 在应有的时间间隔内对未听说过的路由表对等点执行 ping 操作，如果它们不回复，则将其逐出。
func (r *RtRefreshManager) pingAndEvictPeers(ctx context.Context) {
	ctx, span := internal.StartSpan(ctx, "RefreshManager.PingAndEvictPeers")
	defer span.End()

	var peersChecked int
	var alive int64
	var wg sync.WaitGroup
	peers := r.rt.GetPeerInfos()
	for _, ps := range peers {
		if time.Since(ps.LastSuccessfulOutboundQueryAt) <= r.successfulOutboundQueryGracePeriod {
			continue
		}

		peersChecked++
		wg.Add(1)
		go func(ps kbucket.PeerInfo) {
			defer wg.Done()

			livelinessCtx, cancel := context.WithTimeout(ctx, peerPingTimeout)
			defer cancel()
			peerIdStr := ps.Id.String()
			livelinessCtx, span := internal.StartSpan(livelinessCtx, "RefreshManager.PingAndEvictPeers.worker", trace.WithAttributes(attribute.String("peer", peerIdStr)))
			defer span.End()

			if err := r.h.Connect(livelinessCtx, peer.AddrInfo{ID: ps.Id}); err != nil {
				logrus.Debugln("evicting peer after failed connection", "peer", peerIdStr, "error", err)
				span.RecordError(err)
				r.rt.RemovePeer(ps.Id)
				return
			}

			if err := r.refreshPingFnc(livelinessCtx, ps.Id); err != nil {
				logrus.Debugln("evicting peer after failed ping", "peer", peerIdStr, "error", err)
				span.RecordError(err)
				r.rt.RemovePeer(ps.Id)
				return
			}

			atomic.AddInt64(&alive, 1)
		}(ps)
	}
	wg.Wait()

	span.SetAttributes(attribute.Int("NumPeersChecked", peersChecked), attribute.Int("NumPeersSkipped", len(peers)-peersChecked), attribute.Int64("NumPeersAlive", alive))
}

func (r *RtRefreshManager) loop() {
	defer r.refcount.Done()

	var refreshTickrCh <-chan time.Time
	if r.enableAutoRefresh {
		err := r.doRefresh(r.ctx, true)
		if err != nil {
			logrus.Debugln("failed when refreshing routing table", err)
		}
		t := time.NewTicker(r.refreshInterval)
		defer t.Stop()
		refreshTickrCh = t.C
	}

	for {
		var waiting []chan<- error
		var forced bool
		select {
		case <-refreshTickrCh:
		case triggerRefreshReq := <-r.triggerRefresh:
			if triggerRefreshReq.respCh != nil {
				waiting = append(waiting, triggerRefreshReq.respCh)
			}
			forced = forced || triggerRefreshReq.forceCplRefresh
		case <-r.ctx.Done():
			return
		}

		// Batch multiple refresh requests if they're all waiting at the same time.
	OuterLoop:
		for {
			select {
			case triggerRefreshReq := <-r.triggerRefresh:
				if triggerRefreshReq.respCh != nil {
					waiting = append(waiting, triggerRefreshReq.respCh)
				}
				forced = forced || triggerRefreshReq.forceCplRefresh
			default:
				break OuterLoop
			}
		}

		ctx, span := internal.StartSpan(r.ctx, "RefreshManager.Refresh")

		r.pingAndEvictPeers(ctx)

		// Query for self and refresh the required buckets
		err := r.doRefresh(ctx, forced)
		for _, w := range waiting {
			w <- err
			close(w)
		}
		if err != nil {
			logrus.Debugln("failed when refreshing routing table", "error", err)
		}

		span.End()
	}
}

func (r *RtRefreshManager) doRefresh(ctx context.Context, forceRefresh bool) error {
	ctx, span := internal.StartSpan(ctx, "RefreshManager.doRefresh")
	defer span.End()

	var merr error

	if err := r.queryForSelf(ctx); err != nil {
		merr = multierror.Append(merr, err)
	}

	refreshCpls := r.rt.GetTrackedCplsForRefresh()

	rfnc := func(cpl uint) (err error) {
		if forceRefresh {
			err = r.refreshCpl(ctx, cpl)
		} else {
			err = r.refreshCplIfEligible(ctx, cpl, refreshCpls[cpl])
		}
		return
	}

	for c := range refreshCpls {
		cpl := uint(c)
		if err := rfnc(cpl); err != nil {
			merr = multierror.Append(merr, err)
		} else {
			// If we see a gap at a Cpl in the Routing table, we ONLY refresh up until the maximum cpl we
			// have in the Routing Table OR (2 * (Cpl+ 1) with the gap), whichever is smaller.
			// This is to prevent refreshes for Cpls that have no peers in the network but happen to be before a very high max Cpl
			// for which we do have peers in the network.
			// The number of 2 * (Cpl + 1) can be proved and a proof would have been written here if the programmer
			// had paid more attention in the Math classes at university.
			// So, please be patient and a doc explaining it will be published soon.
			if r.rt.NPeersForCpl(cpl) == 0 {
				lastCpl := min(2*(c+1), len(refreshCpls)-1)
				for i := c + 1; i < lastCpl+1; i++ {
					if err := rfnc(uint(i)); err != nil {
						merr = multierror.Append(merr, err)
					}
				}
				return merr
			}
		}
	}

	select {
	case r.refreshDoneCh <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	return merr
}

func min(a int, b int) int {
	if a <= b {
		return a
	}

	return b
}

func (r *RtRefreshManager) refreshCplIfEligible(ctx context.Context, cpl uint, lastRefreshedAt time.Time) error {
	if time.Since(lastRefreshedAt) <= r.refreshInterval {
		logrus.Debugf("not running refresh for cpl %d as time since last refresh not above interval", cpl)
		return nil
	}

	return r.refreshCpl(ctx, cpl)
}

func (r *RtRefreshManager) refreshCpl(ctx context.Context, cpl uint) error {
	ctx, span := internal.StartSpan(ctx, "RefreshManager.refreshCpl", trace.WithAttributes(attribute.Int("cpl", int(cpl))))
	defer span.End()

	// gen a key for the query to refresh the cpl
	key, err := r.refreshKeyGenFnc(cpl)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to generated query key for cpl=%d, err=%s", cpl, err)
	}

	logrus.Debugf("starting refreshing cpl %d with key %s (routing table size was %d)",
		cpl, loggableRawKeyString(key), r.rt.Size())

	if err := r.runRefreshDHTQuery(ctx, key); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to refresh cpl=%d, err=%s", cpl, err)
	}

	sz := r.rt.Size()
	logrus.Debugf("finished refreshing cpl %d, routing table size is now %d", cpl, sz)
	span.SetAttributes(attribute.Int("NewSize", sz))
	return nil
}

func (r *RtRefreshManager) queryForSelf(ctx context.Context) error {
	ctx, span := internal.StartSpan(ctx, "RefreshManager.queryForSelf")
	defer span.End()

	if err := r.runRefreshDHTQuery(ctx, string(r.dhtPeerId)); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to query for self, err=%s", err)
	}
	return nil
}

func (r *RtRefreshManager) runRefreshDHTQuery(ctx context.Context, key string) error {
	queryCtx, cancel := context.WithTimeout(ctx, r.refreshQueryTimeout)
	defer cancel()

	err := r.refreshQueryFnc(queryCtx, key)

	if err == nil || (err == context.DeadlineExceeded && queryCtx.Err() == context.DeadlineExceeded) {
		return nil
	}

	return err
}

type loggableRawKeyString string

func (lk loggableRawKeyString) String() string {
	k := string(lk)

	if len(k) == 0 {
		return k
	}

	encStr := base32.RawStdEncoding.EncodeToString([]byte(k))

	return encStr
}
