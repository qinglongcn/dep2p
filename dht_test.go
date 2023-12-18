// kad-dht

package dep2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
	"github.com/multiformats/go-multistream"
	"github.com/sirupsen/logrus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	test "github.com/bpfs/dep2p/internal/testing"

	u "github.com/ipfs/boxo/util"
	"github.com/ipfs/go-cid"
	detectrace "github.com/ipfs/go-detect-race"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
)

var testCaseCids []cid.Cid

func init() {
	// 初始化测试用例的 Cid 列表
	for i := 0; i < 100; i++ {
		v := fmt.Sprintf("%d -- value", i)

		var newCid cid.Cid
		switch i % 3 {
		case 0:
			// 使用 cid.NewCidV0 创建一个 Cid
			mhv := u.Hash([]byte(v))
			newCid = cid.NewCidV0(mhv)
		case 1:
			// 使用 cid.NewCidV1 创建一个 Cid
			mhv := u.Hash([]byte(v))
			newCid = cid.NewCidV1(cid.DagCBOR, mhv)
		case 2:
			// 使用 cid.NewCidV1 创建一个使用原始编码的 Cid
			rawMh := make([]byte, 12)
			binary.PutUvarint(rawMh, cid.Raw)
			binary.PutUvarint(rawMh[1:], 10)
			copy(rawMh[2:], []byte(v)[:10])
			_, mhv, err := multihash.MHFromBytes(rawMh)
			if err != nil {
				panic(err)
			}
			newCid = cid.NewCidV1(cid.Raw, mhv)
		}
		testCaseCids = append(testCaseCids, newCid)
	}
}

// blankValidator 是一个空的验证器结构体
type blankValidator struct{}

// Validate 是 blankValidator 结构体的方法，用于验证数据的有效性
func (blankValidator) Validate(_ string, _ []byte) error {
	return nil
}

// Select 是 blankValidator 结构体的方法，用于选择数据
func (blankValidator) Select(_ string, _ [][]byte) (int, error) {
	return 0, nil
}

// testAtomicPutValidator 是一个测试用的原子写入验证器结构体，继承自 test.TestValidator
type testAtomicPutValidator struct {
	test.TestValidator
}

// Select 方法根据规则选择条目。
// 它遍历传入的字节切片，选择具有最高最后一个字节的条目。
// 如果找不到有效的条目，则返回错误。
func (testAtomicPutValidator) Select(_ string, bs [][]byte) (int, error) {
	index := -1
	max := uint8(0)
	for i, b := range bs {
		if bytes.Equal(b, []byte("valid")) {
			// 如果找到 "valid" 条目，将其索引记录下来
			if index == -1 {
				index = i
			}
			continue
		}

		str := string(b)
		n := str[len(str)-1]
		if n > max {
			// 如果当前条目的最后一个字节大于最大值，更新最大值和索引
			max = n
			index = i
		}
	}

	if index == -1 {
		// 如果找不到有效的条目，返回错误
		return -1, fmt.Errorf("no rec found")
	}

	// 返回选择的条目的索引
	return index, nil
}

// var testPrefix = ProtocolPrefix("/test") // testPrefix是一个变量，表示协议的前缀为 "/test"。
// setupDHT 函数用于设置 DHT，并返回一个 DeP2PDHT 对象。
// 它接受一个上下文对象 ctx、一个测试对象 t、一个布尔值 client，以及一系列选项参数 options。
func setupDHT(ctx context.Context, t *testing.T, client bool, options ...Option) *DeP2PDHT {
	// 设置基本选项
	baseOpts := []Option{
		// testPrefix, // 使用测试前缀
		// NamespacedValidator("v", blankValidator{}), // 使用空白验证器
		// DisableAutoRefresh(),                       // 禁用自动刷新
	}

	// 根据 client 参数设置不同的模式选项
	if client {
		baseOpts = append(baseOpts, Mode(ModeClient)) // 客户端模式
	} else {
		baseOpts = append(baseOpts, Mode(ModeServer)) // 服务器模式
	}

	// 创建基于 Swarm 的主机对象
	host, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
	require.NoError(t, err)
	host.Start()
	t.Cleanup(func() { host.Close() })

	// 创建 DeP2PDHT 对象
	d, err := New(ctx, host, append(baseOpts, options...)...)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })

	// 返回创建的 DeP2PDHT 对象
	return d
}

// setupDHTS 函数用于设置多个 DHT，并返回一个包含这些 DHT 对象的切片。
// 它接受一个测试对象 t、一个上下文对象 ctx、一个整数 n，表示要设置的 DHT 的数量，以及一系列选项参数 options。
func setupDHTS(t *testing.T, ctx context.Context, n int, options ...Option) []*DeP2PDHT {
	// 创建用于存储地址、DHT 对象和对等节点的切片
	addrs := make([]ma.Multiaddr, n)
	dhts := make([]*DeP2PDHT, n)
	peers := make([]peer.ID, n)

	// 创建用于检查地址和对等节点是否重复的映射
	sanityAddrsMap := make(map[string]struct{})
	sanityPeersMap := make(map[string]struct{})

	// 设置 n 个 DHT
	for i := 0; i < n; i++ {
		// 调用 setupDHT 函数设置单个 DHT
		dhts[i] = setupDHT(ctx, t, false, options...)
		peers[i] = dhts[i].PeerID()
		addrs[i] = dhts[i].host.Addrs()[0]

		// 检查地址是否重复
		if _, lol := sanityAddrsMap[addrs[i].String()]; lol {
			t.Fatal("While setting up DHTs address got duplicated.")
		} else {
			sanityAddrsMap[addrs[i].String()] = struct{}{}
		}

		// 检查对等节点是否重复
		if _, lol := sanityPeersMap[peers[i].String()]; lol {
			t.Fatal("While setting up DHTs peerid got duplicated.")
		} else {
			sanityPeersMap[peers[i].String()] = struct{}{}
		}
	}

	// 返回设置的 DHT 对象切片
	return dhts
}

// connectNoSync 函数用于在两个 DHT 节点之间建立连接，但不进行同步。
// 它接受一个测试对象 t、一个上下文对象 ctx，以及两个 DeP2PDHT 对象 a 和 b。
func connectNoSync(t *testing.T, ctx context.Context, a, b *DeP2PDHT) {
	t.Helper()

	// 获取节点 b 的 ID 和地址
	idB := b.self
	addrB := b.peerstore.Addrs(idB)

	// 检查是否设置了本地地址
	if len(addrB) == 0 {
		t.Fatal("peers setup incorrectly: no local address")
	}

	// 使用节点 a 的主机对象连接到节点 b
	if err := a.host.Connect(ctx, peer.AddrInfo{ID: idB, Addrs: addrB}); err != nil {
		t.Fatal(err)
	}
}

// wait 函数用于等待两个 DHT 节点之间的连接。
// 它接受一个测试对象 t、一个上下文对象 ctx，以及两个 DeP2PDHT 对象 a 和 b。
func wait(t *testing.T, ctx context.Context, a, b *DeP2PDHT) {
	t.Helper()

	// 循环直到收到连接通知。
	// 在高负载情况下，这可能不会像我们希望的那样立即发生。
	for a.routingTable.Find(b.self) == "" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond * 5):
		}
	}
} // connect 函数用于在两个 DHT 节点之间建立连接，并等待连接完成。
// 它接受一个测试对象 t、一个上下文对象 ctx，以及两个 DeP2PDHT 对象 a 和 b。
func connect(t *testing.T, ctx context.Context, a, b *DeP2PDHT) {
	t.Helper()

	// 调用 connectNoSync 函数建立连接
	connectNoSync(t, ctx, a, b)

	// 等待连接完成
	wait(t, ctx, a, b)
	wait(t, ctx, b, a)
}

// bootstrap 函数用于引导一组 DHT 节点。
// 它接受一个测试对象 t、一个上下文对象 ctx，以及一个 DeP2PDHT 对象数组 dhts。
func bootstrap(t *testing.T, ctx context.Context, dhts []*DeP2PDHT) {
	// 创建一个新的上下文对象，并在函数结束时取消该上下文
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logrus.Debugf("refreshing DHTs routing tables...")

	// 随机选择一个起始节点，以减少偏差
	start := rand.Intn(len(dhts))

	// 循环刷新每个 DHT 节点的路由表
	for i := range dhts {
		// 获取当前要刷新的 DHT 节点
		dht := dhts[(start+i)%len(dhts)]

		select {
		case err := <-dht.RefreshRoutingTable():
			// 如果刷新过程中出现错误，则打印错误日志
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// TestRefreshMultiple 函数用于测试刷新多个 DHT 节点的路由表。
// 它创建了一个上下文对象和一组 DHT 节点，然后在其中的一个节点上多次调用 RefreshRoutingTable 函数。
// 最后，它检查每次调用是否都会返回，并在超时时打印错误日志。
func TestRefreshMultiple(t *testing.T) {
	// 创建一个具有超时时间的上下文对象
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()

	// 设置5个 DHT 节点
	dhts := setupDHTS(t, ctx, 5)
	defer func() {
		for _, dht := range dhts {
			dht.Close()
			defer dht.host.Close()
		}
	}()

	// 将除第一个节点外的其他节点与第一个节点建立连接
	for _, dht := range dhts[1:] {
		connect(t, ctx, dhts[0], dht)
	}

	// 在第一个节点上多次调用 RefreshRoutingTable 函数
	a := dhts[0].RefreshRoutingTable()
	time.Sleep(time.Nanosecond)
	b := dhts[0].RefreshRoutingTable()
	time.Sleep(time.Nanosecond)
	c := dhts[0].RefreshRoutingTable()

	// 确保每次调用最终都会返回
	select {
	case <-a:
	case <-ctx.Done():
		t.Fatal("第一个通道没有发出信号")
	}
	select {
	case <-b:
	case <-ctx.Done():
		t.Fatal("第二个通道没有发出信号")
	}
	select {
	case <-c:
	case <-ctx.Done():
		t.Fatal("第三个通道没有发出信号")
	}
}

// TestContextShutDown 函数用于测试关闭 DHT 时的上下文状态。
// 在函数中，首先创建一个上下文对象，并在函数结束时取消上下文。
// 然后，创建一个 DHT 节点。
// 接下来，检查上下文是否处于活动状态，期望上下文未完成。
// 关闭 DHT 节点。
// 最后，再次检查上下文状态，期望上下文已完成。
func TestContextShutDown(t *testing.T) {
	t.Skip("This test is flaky, see https://github.com/libp2p/go-libp2p-kad-dht/issues/724.")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 创建一个 DHT 节点
	dht := setupDHT(ctx, t, false)

	// 上下文应该处于活动状态
	select {
	case <-dht.Context().Done():
		t.Fatal("上下文不应该已完成")
	default:
	}

	// 关闭 DHT 节点
	require.NoError(t, dht.Close())

	// 现在上下文应该已完成
	select {
	case <-dht.Context().Done():
	default:
		t.Fatal("上下文应该已完成")
	}
}

// 如果 minPeers 或 avgPeers 为 0，则不进行测试。
func waitForWellFormedTables(t *testing.T, dhts []*DeP2PDHT, minPeers, avgPeers int, timeout time.Duration) {
	// 测试“良好形成的”路由表（每个路由表中有 >= minPeers 个对等节点）
	t.Helper()

	timeoutA := time.After(timeout)
	for {
		select {
		case <-timeoutA:
			t.Errorf("在 %s 后未能达到良好形成的路由表", timeout)
			return
		case <-time.After(5 * time.Millisecond):
			if checkForWellFormedTablesOnce(t, dhts, minPeers, avgPeers) {
				// 成功
				return
			}
		}
	}
}

// checkForWellFormedTablesOnce 函数用于检查路由表是否良好形成。
// 它接受一个测试对象 `t`，一个 `DeP2PDHT` 节点数组 `dhts`，最小对等节点数 `minPeers` 和平均对等节点数 `avgPeers`。
// 函数首先计算总对等节点数 `totalPeers`，然后遍历每个 `dht` 节点。
// 对于每个节点，获取其路由表的大小 `rtlen`，并将其加到 `totalPeers` 中。
// 如果 `minPeers` 大于 0 且 `rtlen` 小于 `minPeers`，则表示路由表不满足最小对等节点数的要求，返回 false。
// 计算实际的平均对等节点数 `actualAvgPeers`，如果 `avgPeers` 大于 0 且 `actualAvgPeers` 小于 `avgPeers`，则返回 false。
// 如果路由表满足要求，返回 true。

func checkForWellFormedTablesOnce(t *testing.T, dhts []*DeP2PDHT, minPeers, avgPeers int) bool {
	t.Helper()
	totalPeers := 0
	for _, dht := range dhts {
		rtlen := dht.routingTable.Size()
		totalPeers += rtlen
		if minPeers > 0 && rtlen < minPeers {
			//t.Logf("路由表 %s 只有 %d 个对等节点（应该有 >%d）", dht.self, rtlen, minPeers)
			return false
		}
	}
	actualAvgPeers := totalPeers / len(dhts)
	t.Logf("平均路由表大小：%d", actualAvgPeers)
	if avgPeers > 0 && actualAvgPeers < avgPeers {
		t.Logf("平均路由表大小：%d < %d", actualAvgPeers, avgPeers)
		return false
	}
	return true
}

// printRoutingTables 函数用于打印路由表。
// 它接受一个 `DeP2PDHT` 节点数组 `dhts`。
// 函数遍历每个 `dht` 节点，并打印其路由表。

func printRoutingTables(dhts []*DeP2PDHT) {
	// 现在应该填满了路由表，让我们来检查它们。
	fmt.Printf("检查 %d 个节点的路由表\n", len(dhts))
	for _, dht := range dhts {
		fmt.Printf("检查 %s 的路由表\n", dht.self)
		dht.routingTable.Print()
		fmt.Println("")
	}
}

// TestRefresh 函数用于测试刷新（refresh）操作。
// 如果测试运行时间较短，则跳过测试。
// 创建一个包含 30 个 DHT 节点的环境，并在测试结束时关闭这些节点。
// 打印连接了多少个 DHT 节点。
// 将这些 DHT 节点按顺序连接成环。
// 等待 100 毫秒。
// 多次进行引导操作，直到路由表形成良好。
// 如果一次引导操作后，检查路由表是否满足最小对等节点数为 7 且平均对等节点数为 10，则跳出循环。
// 每次引导操作之间暂停 50 微秒。
// 如果启用了调试模式，则打印路由表。
func TestRefresh(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nDHTs := 30
	dhts := setupDHTS(t, ctx, nDHTs)
	defer func() {
		for i := 0; i < nDHTs; i++ {
			dhts[i].Close()
			defer dhts[i].host.Close()
		}
	}()

	t.Logf("连接 %d 个 DHT 节点成环", nDHTs)
	for i := 0; i < nDHTs; i++ {
		connect(t, ctx, dhts[i], dhts[(i+1)%len(dhts)])
	}

	<-time.After(100 * time.Millisecond)
	t.Logf("引导它们，使它们相互发现 %d 次", nDHTs)

	for {
		bootstrap(t, ctx, dhts)

		if checkForWellFormedTablesOnce(t, dhts, 7, 10) {
			break
		}

		time.Sleep(time.Microsecond * 50)
	}

	if u.Debug {
		// 现在应该填满了路由表，让我们来检查它们。
		printRoutingTables(dhts)
	}
}

// TestRefreshBelowMinRTThreshold 函数用于测试当路由表中的对等节点数低于最小对等节点数阈值时的刷新（refresh）操作。
// 创建一个包含 3 个 DHT 节点的环境，并在测试结束时关闭这些节点。
// 将节点 A 设置为自动引导模式，并等待它与节点 B 和节点 C 建立连接。
// 执行一次路由表刷新操作，并等待一轮完成，确保节点 A 与节点 B 和节点 C 都连接成功。
// 创建两个新的 DHT 节点 D 和 E，并将它们相互连接。
// 将节点 D 连接到节点 A，触发节点 A 的最小对等节点数阈值，从而进行引导操作。
// 由于默认的引导扫描间隔为 30 分钟 - 1 小时，我们可以确定如果进行了引导操作，那是因为最小对等节点数阈值被触发了。
// 等待引导操作完成，并确保节点 A 的路由表中有 4 个对等节点，包括节点 E。
// 等待 100 毫秒。
// 断言节点 A 的路由表中存在节点 E。
func TestRefreshBelowMinRTThreshold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 创建一个新的主机
	host, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
	require.NoError(t, err)
	host.Start()

	// 在节点 A 上启用自动引导
	dhtA, err := New(
		ctx,
		host,
		// testPrefix,
		Mode(ModeServer),
		// NamespacedValidator("v", blankValidator{}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// 创建节点 B 和节点 C
	dhtB := setupDHT(ctx, t, false)
	dhtC := setupDHT(ctx, t, false)

	defer func() {
		// 关闭节点 A、B 和节点 C
		dhtA.Close()
		dhtA.host.Close()

		dhtB.Close()
		dhtB.host.Close()

		dhtC.Close()
		dhtC.host.Close()
	}()

	// 将节点 A 与节点 B 和节点 C 连接
	connect(t, ctx, dhtA, dhtB)
	connect(t, ctx, dhtB, dhtC)

	// 对节点 A 执行一次路由表刷新操作，并等待一轮完成，确保节点 A 与节点 B 和节点 C 都连接成功
	dhtA.RefreshRoutingTable()
	waitForWellFormedTables(t, []*DeP2PDHT{dhtA}, 2, 2, 20*time.Second)

	// 创建两个新的节点 D 和节点 E
	dhtD := setupDHT(ctx, t, false)
	dhtE := setupDHT(ctx, t, false)

	// 将节点 D 和节点 E 相互连接
	connect(t, ctx, dhtD, dhtE)
	defer func() {
		// 关闭节点 D 和节点 E
		dhtD.Close()
		dhtD.host.Close()

		dhtE.Close()
		dhtE.host.Close()
	}()

	// 将节点 D 连接到节点 A，触发节点 A 的最小对等节点数阈值，从而进行引导操作
	connect(t, ctx, dhtA, dhtD)

	// 由于上述引导操作，节点 A 也会发现节点 E
	waitForWellFormedTables(t, []*DeP2PDHT{dhtA}, 4, 4, 10*time.Second)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, dhtE.self, dhtA.routingTable.Find(dhtE.self), "A 的路由表应包含节点 E！")
}

// TestQueryWithEmptyRTShouldNotPanic 函数用于测试在空的路由表情况下执行查询操作不应引发恐慌。
// 创建一个新的 DHT 节点并设置上下文。
// 在空的路由表中执行 FindProviders 操作，期望返回空结果。
// 在空的路由表中执行 GetClosestPeers 操作，期望返回空结果并且产生 ErrLookupFailure 错误。
// 在空的路由表中执行 GetValue 操作，期望返回空结果并且产生错误。
// 在空的路由表中执行 SearchValue 操作，期望返回空的通道并且没有产生错误。
// 在空的路由表中执行 Provide 操作，期望产生 ErrLookupFailure 错误。
// func TestQueryWithEmptyRTShouldNotPanic(t *testing.T) {
// 	ctx := context.Background()

// 	// 创建一个新的 DHT 节点
// 	d := setupDHT(ctx, t, false)

// 	// 在空的路由表中执行 FindProviders 操作，期望返回空结果
// 	ps, _ := d.FindProviders(ctx, testCaseCids[0])
// 	require.Empty(t, ps)

// 	// 在空的路由表中执行 GetClosestPeers 操作，期望返回空结果并且产生 ErrLookupFailure 错误
// 	pc, err := d.GetClosestPeers(ctx, "key")
// 	require.Nil(t, pc)
// 	require.Equal(t, kb.ErrLookupFailure, err)

// 	// 在空的路由表中执行 GetValue 操作，期望返回空结果并且产生错误
// 	best, err := d.GetValue(ctx, "key")
// 	require.Empty(t, best)
// 	require.Error(t, err)

// 	// 在空的路由表中执行 SearchValue 操作，期望返回空的通道并且没有产生错误
// 	bchan, err := d.SearchValue(ctx, "key")
// 	require.Empty(t, bchan)
// 	require.NoError(t, err)

// 	// 在空的路由表中执行 Provide 操作，期望产生 ErrLookupFailure 错误
// 	err = d.Provide(ctx, testCaseCids[0], true)
// 	require.Equal(t, kb.ErrLookupFailure, err)
// }

// TestPeriodicRefresh 用于测试定期刷新操作。
func TestPeriodicRefresh(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	if runtime.GOOS == "windows" {
		t.Skip("由于 #760，跳过测试")
	}
	if detectrace.WithRace() {
		t.Skip("由于竞争检测器的最大 goroutine 数量，跳过测试")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nDHTs := 30
	dhts := setupDHTS(t, ctx, nDHTs)
	defer func() {
		for i := 0; i < nDHTs; i++ {
			dhts[i].Close()
			defer dhts[i].host.Close()
		}
	}()

	t.Logf("DHTs 未连接。%d", nDHTs)
	for _, dht := range dhts {
		rtlen := dht.routingTable.Size()
		if rtlen > 0 {
			t.Errorf("对于 %s，路由表应该有 0 个对等节点，但实际有 %d 个", dht.self, rtlen)
		}
	}

	for i := 0; i < nDHTs; i++ {
		connect(t, ctx, dhts[i], dhts[(i+1)%len(dhts)])
	}

	t.Logf("DHTs 现在连接到其他 1-2 个节点。%d", nDHTs)
	for _, dht := range dhts {
		rtlen := dht.routingTable.Size()
		if rtlen > 2 {
			t.Errorf("对于 %s，路由表最多应该有 2 个对等节点，但实际有 %d 个", dht.self, rtlen)
		}
	}

	if u.Debug {
		printRoutingTables(dhts)
	}

	t.Logf("引导它们以便它们互相发现。%d", nDHTs)
	var wg sync.WaitGroup
	for _, dht := range dhts {
		wg.Add(1)
		go func(d *DeP2PDHT) {
			<-d.RefreshRoutingTable()
			wg.Done()
		}(dht)
	}

	wg.Wait()
	// 这是异步的，我们不知道它何时完成一个周期，因此一直检查
	// 直到路由表看起来更好，或者超过长时间的超时时间，表示失败。
	waitForWellFormedTables(t, dhts, 7, 10, 20*time.Second)

	if u.Debug {
		printRoutingTables(dhts)
	}
}

// TestProvidesMany 用于测试提供者功能。
func TestProvidesMany(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("由于 #760，跳过测试")
	}
	if detectrace.WithRace() {
		t.Skip("由于竞争检测器的最大 goroutine 数量，跳过测试")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nDHTs := 40
	dhts := setupDHTS(t, ctx, nDHTs)
	defer func() {
		for i := 0; i < nDHTs; i++ {
			dhts[i].Close()
			defer dhts[i].host.Close()
		}
	}()

	t.Logf("连接 %d 个 DHT 节点形成环", nDHTs)
	for i := 0; i < nDHTs; i++ {
		connect(t, ctx, dhts[i], dhts[(i+1)%len(dhts)])
	}

	<-time.After(100 * time.Millisecond)
	t.Logf("引导它们以便它们互相发现。%d", nDHTs)
	ctxT, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	bootstrap(t, ctxT, dhts)

	if u.Debug {
		// 路由表现在应该已经填满了。让我们检查一下。
		t.Logf("检查 %d 的路由表", nDHTs)
		for _, dht := range dhts {
			fmt.Printf("检查 %s 的路由表\n", dht.self)
			dht.routingTable.Print()
			fmt.Println("")
		}
	}

	providers := make(map[cid.Cid]peer.ID)

	d := 0
	for _, c := range testCaseCids {
		d = (d + 1) % len(dhts)
		dht := dhts[d]
		providers[c] = dht.self

		t.Logf("为 %s 声明提供者", c)
		if err := dht.Provide(ctx, c, true); err != nil {
			t.Fatal(err)
		}
	}

	// 这个超时是为了什么？之前是 60ms。
	time.Sleep(time.Millisecond * 6)

	errchan := make(chan error)

	ctxT, cancel = context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	getProvider := func(dht *DeP2PDHT, k cid.Cid) {
		defer wg.Done()

		expected := providers[k]

		provchan := dht.FindProvidersAsync(ctxT, k, 1)
		select {
		case prov := <-provchan:
			actual := prov.ID
			if actual == "" {
				errchan <- fmt.Errorf("获取到了空的提供者 (%s at %s)", k, dht.self)
			} else if actual != expected {
				errchan <- fmt.Errorf("获取到了错误的提供者 (%s != %s) (%s at %s)",
					expected, actual, k, dht.self)
			}
		case <-ctxT.Done():
			errchan <- fmt.Errorf("未获取到提供者 (%s at %s)", k, dht.self)
		}
	}

	for _, c := range testCaseCids {
		// 每个节点都应该能够找到它...
		for _, dht := range dhts {
			logrus.Debugf("获取 %s 的提供者：%s", c, dht.self)
			wg.Add(1)
			go getProvider(dht, c)
		}
	}

	// 我们需要这个是因为要打印错误
	go func() {
		wg.Wait()
		close(errchan)
	}()

	for err := range errchan {
		t.Error(err)
	}
}

// TestProvidesAsync 用于测试异步提供者功能。
func TestProvidesAsync(t *testing.T) {
	// t.Skip("跳过测试以调试其他问题")
	if testing.Short() {
		t.SkipNow()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dhts := setupDHTS(t, ctx, 4)
	defer func() {
		for i := 0; i < 4; i++ {
			dhts[i].Close()
			defer dhts[i].host.Close()
		}
	}()

	connect(t, ctx, dhts[0], dhts[1])
	connect(t, ctx, dhts[1], dhts[2])
	connect(t, ctx, dhts[1], dhts[3])

	err := dhts[3].Provide(ctx, testCaseCids[0], true)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond * 60)

	ctxT, cancel := context.WithTimeout(ctx, time.Millisecond*300)
	defer cancel()
	provs := dhts[0].FindProvidersAsync(ctxT, testCaseCids[0], 5)
	select {
	case p, ok := <-provs:
		if !ok {
			t.Fatal("提供者通道已关闭...")
		}
		if p.ID == "" {
			t.Fatal("获取到了空的提供者！")
		}
		if p.ID != dhts[3].self {
			t.Fatalf("获取到了提供者，但不是正确的提供者。%s", p)
		}
	case <-ctxT.Done():
		t.Fatal("未获取到提供者")
	}
}

// TestUnfindablePeer 用于测试无法找到对等节点的情况。
// func TestUnfindablePeer(t *testing.T) {
// 	// 跳过测试以调试其他问题
// 	if testing.Short() {
// 		t.SkipNow()
// 	}

// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 设置测试环境并创建 4 个 DHT 节点
// 	dhts := setupDHTS(t, ctx, 4)
// 	defer func() {
// 		for i := 0; i < 4; i++ {
// 			dhts[i].Close()
// 			dhts[i].Host().Close()
// 		}
// 	}()

// 	// 连接节点之间的对等节点
// 	connect(t, ctx, dhts[0], dhts[1])
// 	connect(t, ctx, dhts[1], dhts[2])
// 	connect(t, ctx, dhts[2], dhts[3])

// 	// 将 DHT 1 的 DHT 2 的地址设置为错误地址
// 	dhts[1].host.Peerstore().ClearAddrs(dhts[2].PeerID())
// 	dhts[1].host.Peerstore().AddAddr(dhts[2].PeerID(), dhts[0].Host().Addrs()[0], time.Minute)

// 	// 创建一个超时上下文和取消函数
// 	ctxT, cancel := context.WithTimeout(ctx, time.Second)
// 	defer cancel()

// 	// 在节点 0 中查找对等节点 DHT 3
// 	_, err := dhts[0].FindPeer(ctxT, dhts[3].PeerID())

// 	// 断言是否成功找到对等节点
// 	if err == nil {
// 		t.Error("应该无法找到对等节点")
// 	}

// 	// 断言在上下文过期之前是否失败
// 	if ctxT.Err() != nil {
// 		t.Error("在上下文过期之前应该失败")
// 	}
// }

// TestFindPeer 用于测试查找对等节点功能。
// func TestFindPeer(t *testing.T) {
// 	// 跳过测试以调试其他问题
// 	// t.Skip("skipping test to debug another")
// 	if testing.Short() {
// 		t.SkipNow()
// 	}

// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 设置测试环境并创建 4 个 DHT 节点
// 	dhts := setupDHTS(t, ctx, 4)
// 	defer func() {
// 		for i := 0; i < 4; i++ {
// 			dhts[i].Close()
// 			dhts[i].host.Close()
// 		}
// 	}()

// 	// 连接节点之间的对等节点
// 	connect(t, ctx, dhts[0], dhts[1])
// 	connect(t, ctx, dhts[1], dhts[2])
// 	connect(t, ctx, dhts[1], dhts[3])

// 	// 创建一个超时上下文和取消函数
// 	ctxT, cancel := context.WithTimeout(ctx, time.Second)
// 	defer cancel()

// 	// 在节点 0 中查找对等节点 DHT 2
// 	p, err := dhts[0].FindPeer(ctxT, dhts[2].PeerID())
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	// 断言是否成功找到对等节点
// 	if p.ID == "" {
// 		t.Fatal("无法找到对等节点")
// 	}

// 	// 断言找到的对等节点是否与预期相符
// 	if p.ID != dhts[2].PeerID() {
// 		t.Fatal("未找到预期的对等节点")
// 	}
// }

// TestFindPeerWithQueryFilter 用于测试带有查询过滤器的查找对等节点功能。
// func TestFindPeerWithQueryFilter(t *testing.T) {
// 	// 跳过测试以调试其他问题
// 	// t.Skip("skipping test to debug another")
// 	if testing.Short() {
// 		t.SkipNow()
// 	}
// 	if runtime.GOOS == "windows" {
// 		t.Skip("skipping due to #760")
// 	}

// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 创建一个过滤器节点
// 	filteredPeer, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 	require.NoError(t, err)
// 	filteredPeer.Start()
// 	defer filteredPeer.Close()

// 	// 设置测试环境并创建 4 个带有查询过滤器的 DHT 节点
// 	dhts := setupDHTS(t, ctx, 4, QueryFilter(func(_ interface{}, ai peer.AddrInfo) bool {
// 		return ai.ID != filteredPeer.ID()
// 	}))
// 	defer func() {
// 		for i := 0; i < 4; i++ {
// 			dhts[i].Close()
// 			dhts[i].host.Close()
// 		}
// 	}()

// 	// 连接节点之间的对等节点
// 	connect(t, ctx, dhts[0], dhts[1])
// 	connect(t, ctx, dhts[1], dhts[2])
// 	connect(t, ctx, dhts[1], dhts[3])

// 	// 连接过滤器节点和 DHT 2 节点
// 	err = filteredPeer.Connect(ctx, peer.AddrInfo{
// 		ID:    dhts[2].host.ID(),
// 		Addrs: dhts[2].host.Addrs(),
// 	})
// 	require.NoError(t, err)

// 	// 确保成功连接到对等节点
// 	require.Eventually(t, func() bool {
// 		return len(dhts[2].host.Network().ConnsToPeer(filteredPeer.ID())) > 0
// 	}, 5*time.Millisecond, time.Millisecond, "failed to connect to peer")

// 	// 创建一个超时上下文和取消函数
// 	ctxT, cancel := context.WithTimeout(ctx, time.Second)
// 	defer cancel()

// 	// 在节点 0 中查找对等节点 filteredPeer
// 	p, err := dhts[0].FindPeer(ctxT, filteredPeer.ID())
// 	require.NoError(t, err)

// 	// 断言是否成功找到对等节点
// 	require.NotEmpty(t, p.ID, "Failed to find peer.")

// 	// 断言找到的对等节点是否与预期相符
// 	require.Equal(t, filteredPeer.ID(), p.ID, "Didnt find expected peer.")
// }

// TestConnectCollision 用于测试连接冲突的情况。
func TestConnectCollision(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	runTimes := 10

	for rtime := 0; rtime < runTimes; rtime++ {
		logrus.Info("Running Time: ", rtime)

		ctx, cancel := context.WithCancel(context.Background())

		// 设置 DHT A 和 DHT B
		dhtA := setupDHT(ctx, t, false)
		dhtB := setupDHT(ctx, t, false)

		addrA := dhtA.peerstore.Addrs(dhtA.self)[0]
		addrB := dhtB.peerstore.Addrs(dhtB.self)[0]

		peerA := dhtA.self
		peerB := dhtB.self

		errs := make(chan error)
		go func() {
			// 将 DHT B 的地址添加到 DHT A 的 peerstore 中
			dhtA.peerstore.AddAddr(peerB, addrB, peerstore.TempAddrTTL)
			pi := peer.AddrInfo{ID: peerB}
			err := dhtA.host.Connect(ctx, pi)
			errs <- err
		}()
		go func() {
			// 将 DHT A 的地址添加到 DHT B 的 peerstore 中
			dhtB.peerstore.AddAddr(peerA, addrA, peerstore.TempAddrTTL)
			pi := peer.AddrInfo{ID: peerA}
			err := dhtB.host.Connect(ctx, pi)
			errs <- err
		}()

		timeout := time.After(5 * time.Second)
		select {
		case e := <-errs:
			if e != nil {
				t.Fatal(e)
			}
		case <-timeout:
			t.Fatal("Timeout received!")
		}
		select {
		case e := <-errs:
			if e != nil {
				t.Fatal(e)
			}
		case <-timeout:
			t.Fatal("Timeout received!")
		}

		// 关闭 DHT A 和 DHT B
		dhtA.Close()
		dhtB.Close()
		dhtA.host.Close()
		dhtB.host.Close()
		cancel()
	}
}

// TestBadProtoMessages 用于测试处理错误的协议消息的情况。
// func TestBadProtoMessages(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	d := setupDHT(ctx, t, false)

// 	// 创建一个空的消息
// 	nilrec := new(pb.Message)
// 	// 调用 handlePutValue 处理空记录的消息
// 	_, err := d.handlePutValue(ctx, "testpeer", nilrec)
// 	if err == nil {
// 		t.Fatal("should have errored on nil record") // 应该在空记录上发生错误
// 	}
// }

// TestAtomicPut 用于测试原子性 Put 操作的情况。
// func TestAtomicPut(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	d := setupDHT(ctx, t, false)
// 	d.Validator = testAtomicPutValidator{}

// 	// 定义一个函数用于进行记录的 Put 操作
// 	key := "testkey"
// 	putRecord := func(value []byte) error {
// 		// 创建一个 Put 记录
// 		rec := record.MakePutRecord(key, value)
// 		// 创建一个 PUT_VALUE 类型的消息
// 		pmes := pb.NewMessage(pb.Message_PUT_VALUE, rec.Key, 0)
// 		pmes.Record = rec
// 		// 调用 handlePutValue 处理消息
// 		_, err := d.handlePutValue(ctx, "testpeer", pmes)
// 		return err
// 	}

// 	// 对一个有效记录进行 Put 操作
// 	if err := putRecord([]byte("valid")); err != nil {
// 		t.Fatal("should not have errored on a valid record") // 不应该在有效记录上发生错误
// 	}

// 	// 同时对旧值和新值进行并发的 Put 操作
// 	values := [][]byte{[]byte("newer1"), []byte("newer7"), []byte("newer3"), []byte("newer5")}
// 	var wg sync.WaitGroup
// 	for _, v := range values {
// 		wg.Add(1)
// 		go func(v []byte) {
// 			defer wg.Done()
// 			_ = putRecord(v) // 我们预期其中一些操作会失败
// 		}(v)
// 	}
// 	wg.Wait()

// 	// 进行 Get 操作，应返回最新的值
// 	pmes := pb.NewMessage(pb.Message_GET_VALUE, []byte(key), 0)
// 	msg, err := d.handleGetValue(ctx, "testkey", pmes)
// 	if err != nil {
// 		t.Fatalf("should not have errored on final get, but got %+v", err) // 不应该在最终的 Get 操作上发生错误
// 	}
// 	if string(msg.GetRecord().Value) != "newer7" {
// 		t.Fatalf("Expected 'newer7' got '%s'", string(msg.GetRecord().Value)) // 期望得到 'newer7'，但得到了其他值
// 	}
// }

// TestClientModeConnect 用于测试客户端模式下的连接情况。
// func TestClientModeConnect(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 设置 DHT 节点 A
// 	a := setupDHT(ctx, t, false)

// 	// 设置 DHT 节点 B，并启用客户端模式
// 	b := setupDHT(ctx, t, true)

// 	// 在节点 A 和节点 B 之间建立连接，但不进行同步
// 	connectNoSync(t, ctx, a, b)

// 	// 定义一个测试用的 CID 和对等节点 ID
// 	c := testCaseCids[0]
// 	p := peer.ID("TestPeer")

// 	// 在节点 A 上添加一个提供者
// 	a.ProviderStore().AddProvider(ctx, c.Hash(), peer.AddrInfo{ID: p})

// 	// 等待一小段时间，以确保提供者信息被广播
// 	time.Sleep(time.Millisecond * 5) // 以防万一...

// 	// 在节点 B 上查找提供者
// 	provs, err := b.FindProviders(ctx, c)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	// 检查是否找到了提供者
// 	if len(provs) == 0 {
// 		t.Fatal("Expected to get a provider back") // 预期应该找到一个提供者
// 	}

// 	// 检查提供者是否为预期的对等节点
// 	if provs[0].ID != p {
// 		t.Fatal("expected it to be our test peer") // 预期应该是我们的测试对等节点
// 	}

// 	// 检查节点 A 是否未被添加到节点 B 的路由表中
// 	if a.routingTable.Find(b.self) != "" {
// 		t.Fatal("DHT clients should not be added to routing tables") // DHT 客户端不应该被添加到路由表中
// 	}

// 	// 检查节点 B 是否被添加到节点 A 的路由表中
// 	if b.routingTable.Find(a.self) == "" {
// 		t.Fatal("DHT server should have been added to the dht client's routing table") // DHT 服务器应该被添加到 DHT 客户端的路由表中
// 	}
// }

// TestInvalidServer 用于测试无效的服务器。
// func TestInvalidServer(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 设置 DHT 节点 A
// 	a := setupDHT(ctx, t, false)

// 	// 设置 DHT 节点 B，并启用客户端模式
// 	b := setupDHT(ctx, t, true)

// 	// 使节点 B 广播所有 DHT 服务器协议
// 	for _, proto := range a.serverProtocols {
// 		// 挂起每个请求
// 		b.host.SetStreamHandler(proto, func(s network.Stream) {
// 			defer s.Reset() // nolint
// 			<-ctx.Done()
// 		})
// 	}

// 	// 在节点 A 和节点 B 之间建立连接，但不进行同步
// 	connectNoSync(t, ctx, a, b)

// 	// 定义一个测试用的 CID 和对等节点 ID
// 	c := testCaseCids[0]
// 	p := peer.ID("TestPeer")

// 	// 在节点 A 上添加一个提供者
// 	a.ProviderStore().AddProvider(ctx, c.Hash(), peer.AddrInfo{ID: p})

// 	// 等待一小段时间，以确保提供者信息被广播
// 	time.Sleep(time.Millisecond * 5) // 以防万一...

// 	// 在节点 B 上查找提供者
// 	provs, err := b.FindProviders(ctx, c)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	// 检查是否找到了提供者
// 	if len(provs) == 0 {
// 		t.Fatal("Expected to get a provider back") // 预期应该找到一个提供者
// 	}

// 	// 检查提供者是否为预期的对等节点
// 	if provs[0].ID != p {
// 		t.Fatal("expected it to be our test peer") // 预期应该是我们的测试对等节点
// 	}

// 	// 检查节点 A 是否未被添加到节点 B 的路由表中
// 	if a.routingTable.Find(b.self) != "" {
// 		t.Fatal("DHT clients should not be added to routing tables") // DHT 客户端不应该被添加到路由表中
// 	}

// 	// 检查节点 B 是否被添加到节点 A 的路由表中
// 	if b.routingTable.Find(a.self) == "" {
// 		t.Fatal("DHT server should have been added to the dht client's routing table") // DHT 服务器应该被添加到 DHT 客户端的路由表中
// 	}
// }

// // TestClientModeFindPeer 用于测试客户端模式下的查找对等节点。
// func TestClientModeFindPeer(t *testing.T) {
// 	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 	defer cancel()

// 	// 设置 DHT 节点 A
// 	a := setupDHT(ctx, t, false)

// 	// 设置 DHT 节点 B 和 C，并启用客户端模式
// 	b := setupDHT(ctx, t, true)
// 	c := setupDHT(ctx, t, true)

// 	// 在节点 B 和节点 A 之间建立连接，但不进行同步
// 	connectNoSync(t, ctx, b, a)

// 	// 在节点 C 和节点 A 之间建立连接，但不进行同步
// 	connectNoSync(t, ctx, c, a)

// 	// 由于节点 B 和 C 只是客户端，无法使用 `connect` 函数，因此使用 `wait` 函数进行同步
// 	wait(t, ctx, b, a)
// 	wait(t, ctx, c, a)

// 	// 在节点 C 上查找节点 B 的信息
// 	pi, err := c.FindPeer(ctx, b.self)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	// 检查是否找到了节点 B 的地址信息
// 	if len(pi.Addrs) == 0 {
// 		t.Fatal("should have found addresses for node b") // 应该找到节点 B 的地址信息
// 	}

// 	// 在节点 C 上与节点 B 建立连接
// 	err = c.host.Connect(ctx, pi)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// }

// minInt 返回两个整数中的较小值。
// func minInt(a, b int) int {
// 	if a < b {
// 		return a
// 	}
// 	return b
// }

// TestFindPeerQueryMinimal 是一个简化的查找对等节点查询测试。
// func TestFindPeerQueryMinimal(t *testing.T) {
// 	testFindPeerQuery(t, 2, 22, 1)
// }

// TestFindPeerQuery 用于测试查找对等节点查询。
// func TestFindPeerQuery(t *testing.T) {
// 	if detectrace.WithRace() {
// 		t.Skip("由于竞争检测器的最大 goroutine 数量，跳过测试") // 跳过测试，因为检测到了竞争条件
// 	}

// 	if testing.Short() {
// 		t.Skip("在短模式下跳过测试") // 跳过测试，因为处于短模式
// 	}
// 	testFindPeerQuery(t, 5, 40, 3)
// }

// NOTE: 在使用此函数之前，您必须至少拥有 (minRTRefreshThreshold+1) 个测试对等节点。
// func testFindPeerQuery(t *testing.T,
// 	bootstrappers, // 连接到查询节点的节点数量
// 	leafs, // 可能与引导节点建立连接的节点数量
// 	bootstrapConns int, // 每个叶节点应连接到的引导节点数量
// ) {
// 	if runtime.GOOS == "windows" {
// 		t.Skip("由于问题 #760，跳过测试") // 在 Windows 平台上跳过测试
// 	}

// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	dhts := setupDHTS(t, ctx, 1+bootstrappers+leafs, BucketSize(4))
// 	defer func() {
// 		for _, d := range dhts {
// 			d.Close()
// 			d.host.Close()
// 		}
// 	}()

// 	t.Log("连接中")

// 	mrand := rand.New(rand.NewSource(42))
// 	guy := dhts[0]
// 	others := dhts[1:]
// 	for i := 0; i < leafs; i++ {
// 		for _, v := range mrand.Perm(bootstrappers)[:bootstrapConns] {
// 			connectNoSync(t, ctx, others[v], others[bootstrappers+i])
// 		}
// 	}

// 	for i := 0; i < bootstrappers; i++ {
// 		connectNoSync(t, ctx, guy, others[i])
// 	}

// 	t.Log("等待路由表更新")

// 	// 给一些时间让事情安定下来
// 	waitForWellFormedTables(t, dhts, bootstrapConns, bootstrapConns, 5*time.Second)

// 	t.Log("刷新路由表")

// 	var wg sync.WaitGroup
// 	for _, dht := range dhts {
// 		wg.Add(1)
// 		go func(d *DeP2PDHT) {
// 			<-d.RefreshRoutingTable()
// 			wg.Done()
// 		}(dht)
// 	}

// 	wg.Wait()

// 	t.Log("再次等待路由表更新")

// 	// 等待刷新工作完成。至少应有一个桶是满的。
// 	waitForWellFormedTables(t, dhts, 4, 0, 5*time.Second)

// 	var peers []peer.ID
// 	for _, d := range others {
// 		peers = append(peers, d.PeerID())
// 	}

// 	t.Log("查询中")

// 	val := "foobar"
// 	rtval := kb.ConvertKey(val)

// 	outpeers, err := guy.GetClosestPeers(ctx, val)
// 	require.NoError(t, err)

// 	sort.Sort(peer.IDSlice(outpeers))

// 	exp := kb.SortClosestPeers(peers, rtval)[:minInt(guy.bucketSize, len(peers))]
// 	t.Logf("获取到 %d 个对等节点", len(outpeers))
// 	got := kb.SortClosestPeers(outpeers, rtval)

// 	assert.EqualValues(t, exp, got)
// }

// TestFindClosestPeers 函数的用途是测试查找最接近对等节点的功能。
func TestFindClosestPeers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nDHTs := 30
	dhts := setupDHTS(t, ctx, nDHTs)
	defer func() {
		for i := 0; i < nDHTs; i++ {
			dhts[i].Close()
			defer dhts[i].host.Close()
		}
	}()

	t.Logf("连接 %d 个 DHT 节点成环", nDHTs)
	for i := 0; i < nDHTs; i++ {
		connect(t, ctx, dhts[i], dhts[(i+1)%len(dhts)])
	}

	querier := dhts[1]
	peers, err := querier.GetClosestPeers(ctx, "foo")
	if err != nil {
		t.Fatal(err)
	}

	if len(peers) < querier.beta {
		t.Fatalf("获取到错误数量的对等节点（获取到 %d 个，期望至少 %d 个）", len(peers), querier.beta)
	}
}

// TestFixLowPeers 函数的用途是测试修复低对等节点的功能。
func TestFixLowPeers(t *testing.T) {
	ctx := context.Background()

	dhts := setupDHTS(t, ctx, minRTRefreshThreshold+5)

	defer func() {
		for _, d := range dhts {
			d.Close()
			d.Host().Close()
		}
	}()

	mainD := dhts[0]

	// 将主节点与其他节点连接起来
	for _, d := range dhts[1:] {
		mainD.peerstore.AddAddrs(d.self, d.peerstore.Addrs(d.self), peerstore.TempAddrTTL)
		require.NoError(t, mainD.Host().Connect(ctx, peer.AddrInfo{ID: d.self}))
	}

	waitForWellFormedTables(t, []*DeP2PDHT{mainD}, minRTRefreshThreshold, minRTRefreshThreshold+4, 5*time.Second)

	// 对所有节点运行一次路由表刷新
	for _, d := range dhts {
		err := <-d.RefreshRoutingTable()
		require.NoError(t, err)
	}

	// 现在从路由表中删除对等节点，以触发达到阈值的情况
	for _, d := range dhts[3:] {
		mainD.routingTable.RemovePeer(d.self)
	}

	// 由于修复低对等节点的功能，我们仍然会得到足够的对等节点
	waitForWellFormedTables(t, []*DeP2PDHT{mainD}, minRTRefreshThreshold, minRTRefreshThreshold, 5*time.Second)
}

// TestProvideDisabled 用于测试在启用或禁用提供程序的情况下的行为。
// func TestProvideDisabled(t *testing.T) {
// 	k := testCaseCids[0]
// 	kHash := k.Hash()

// 	// 进行三次循环，分别测试三种情况：A启用/B启用、A启用/B禁用、A禁用/B禁用
// 	for i := 0; i < 3; i++ {
// 		enabledA := (i & 0x1) > 0
// 		enabledB := (i & 0x2) > 0
// 		t.Run(fmt.Sprintf("a=%v/b=%v", enabledA, enabledB), func(t *testing.T) {
// 			ctx, cancel := context.WithCancel(context.Background())
// 			defer cancel()

// 			var (
// 				optsA, optsB []Option
// 			)
// 			optsA = append(optsA, ProtocolPrefix("/provMaybeDisabled"))
// 			optsB = append(optsB, ProtocolPrefix("/provMaybeDisabled"))

// 			// 根据情况设置选项，如果禁用则添加 DisableProviders 选项
// 			if !enabledA {
// 				optsA = append(optsA, DisableProviders())
// 			}
// 			if !enabledB {
// 				optsB = append(optsB, DisableProviders())
// 			}

// 			// 设置 DHT 节点 A 和 B
// 			dhtA := setupDHT(ctx, t, false, optsA...)
// 			dhtB := setupDHT(ctx, t, false, optsB...)

// 			defer dhtA.Close()
// 			defer dhtB.Close()
// 			defer dhtA.host.Close()
// 			defer dhtB.host.Close()

// 			// 连接节点 A 和 B
// 			connect(t, ctx, dhtA, dhtB)

// 			// 在节点 B 上提供值 k
// 			err := dhtB.Provide(ctx, k, true)
// 			if enabledB {
// 				if err != nil {
// 					t.Fatal("在节点 B 上应该成功存储值", err)
// 				}
// 			} else {
// 				if err != routing.ErrNotSupported {
// 					t.Fatal("节点 B 不应该存储值", err)
// 				}
// 				_, err = dhtB.FindProviders(ctx, k)
// 				if err != routing.ErrNotSupported {
// 					t.Fatal("在节点 B 上应该失败")
// 				}
// 				provs, _ := dhtB.ProviderStore().GetProviders(ctx, kHash)
// 				if len(provs) != 0 {
// 					t.Fatal("节点 B 不应该找到本地提供者")
// 				}
// 			}

// 			// 在节点 A 上查找提供者
// 			provs, err := dhtA.FindProviders(ctx, k)
// 			if enabledA {
// 				if len(provs) != 0 {
// 					t.Fatal("节点 A 不应该找到提供者")
// 				}
// 			} else {
// 				if err != routing.ErrNotSupported {
// 					t.Fatal("节点 A 不应该找到提供者")
// 				}
// 			}
// 			provAddrs, _ := dhtA.ProviderStore().GetProviders(ctx, kHash)
// 			if len(provAddrs) != 0 {
// 				t.Fatal("节点 A 不应该找到本地提供者")
// 			}
// 		})
// 	}
// }

// TestHandleRemotePeerProtocolChanges 用于测试处理远程对等节点协议更改的行为。
// func TestHandleRemotePeerProtocolChanges(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	os := []Option{
// 		testPrefix,
// 		Mode(ModeServer),
// 		NamespacedValidator("v", blankValidator{}),
// 		DisableAutoRefresh(),
// 	}

// 	// 启动支持 DHT v1 的主机 1
// 	hA, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 	require.NoError(t, err)
// 	hA.Start()
// 	defer hA.Close()
// 	dhtA, err := New(ctx, hA, os...)
// 	require.NoError(t, err)
// 	defer dhtA.Close()

// 	// 启动支持 DHT v1 的主机 2
// 	hB, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 	require.NoError(t, err)
// 	hB.Start()
// 	defer hB.Close()
// 	dhtB, err := New(ctx, hB, os...)
// 	require.NoError(t, err)
// 	defer dhtB.Close()

// 	// 连接节点 A 和 B
// 	connect(t, ctx, dhtA, dhtB)

// 	// 确保节点 A 和 B 在彼此的路由表中
// 	waitForWellFormedTables(t, []*DeP2PDHT{dhtA, dhtB}, 1, 1, 10*time.Second)

// 	// 将节点 B 设置为客户端模式
// 	require.NoError(t, dhtB.setMode(modeClient))

// 	// 这意味着节点 A 应该从其路由表中将节点 B 移除
// 	waitForWellFormedTables(t, []*DeP2PDHT{dhtA}, 0, 0, 10*time.Second)

// 	// 将节点 B 设置为服务器模式
// 	require.NoError(t, dhtB.setMode(modeServer))

// 	// 这意味着由于 fixLowPeers，节点 A 应该再次将节点 B 添加到其路由表中
// 	waitForWellFormedTables(t, []*DeP2PDHT{dhtA}, 1, 1, 10*time.Second)
// }

// // TestGetSetPluggedProtocol 用于测试使用不同协议的节点之间的操作行为。
// func TestGetSetPluggedProtocol(t *testing.T) {
// 	// 测试 "PutValue/GetValue - same protocol" 子测试
// 	t.Run("PutValue/GetValue - same protocol", func(t *testing.T) {
// 		ctx, cancel := context.WithCancel(context.Background())
// 		defer cancel()

// 		os := []Option{
// 			ProtocolPrefix("/esh"), // 设置协议前缀为 "/esh"
// 			Mode(ModeServer),
// 			NamespacedValidator("v", blankValidator{}),
// 			DisableAutoRefresh(),
// 		}

// 		// 启动主机 A，并创建 DHT 节点 A，使用给定的选项
// 		hA, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 		require.NoError(t, err)
// 		hA.Start()
// 		defer hA.Close()
// 		dhtA, err := New(ctx, hA, os...)
// 		require.NoError(t, err)
// 		defer dhtA.Close()

// 		// 启动主机 B，并创建 DHT 节点 B，使用给定的选项
// 		hB, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 		require.NoError(t, err)
// 		hB.Start()
// 		defer hB.Close()
// 		dhtB, err := New(ctx, hB, os...)
// 		require.NoError(t, err)
// 		defer dhtB.Close()

// 		// 连接节点 A 和 B
// 		connect(t, ctx, dhtA, dhtB)

// 		ctxT, cancel := context.WithTimeout(ctx, time.Second)
// 		defer cancel()
// 		err = dhtA.PutValue(ctxT, "/v/cat", []byte("meow")) // 在节点 A 存储值 "/v/cat"，值为 "meow"
// 		require.NoError(t, err)

// 		value, err := dhtB.GetValue(ctxT, "/v/cat") // 从节点 B 获取值 "/v/cat"
// 		require.NoError(t, err)

// 		require.Equal(t, "meow", string(value)) // 确保获取的值与预期值相等
// 	})

// 	// 测试 "DHT routing table for peer A won't contain B if A and B don't use same protocol" 子测试
// 	t.Run("DHT routing table for peer A won't contain B if A and B don't use same protocol", func(t *testing.T) {
// 		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 		defer cancel()

// 		// 启动主机 A，并创建 DHT 节点 A，使用给定的选项
// 		hA, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 		require.NoError(t, err)
// 		hA.Start()
// 		defer hA.Close()
// 		dhtA, err := New(ctx, hA, []Option{
// 			ProtocolPrefix("/esh"), // 设置协议前缀为 "/esh"
// 			Mode(ModeServer),
// 			NamespacedValidator("v", blankValidator{}),
// 			DisableAutoRefresh(),
// 		}...)
// 		require.NoError(t, err)
// 		defer dhtA.Close()

// 		// 启动主机 B，并创建 DHT 节点 B，使用给定的选项
// 		hB, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 		require.NoError(t, err)
// 		hB.Start()
// 		defer hB.Close()
// 		dhtB, err := New(ctx, hB, []Option{
// 			ProtocolPrefix("/lsr"), // 设置协议前缀为 "/lsr"
// 			Mode(ModeServer),
// 			NamespacedValidator("v", blankValidator{}),
// 			DisableAutoRefresh(),
// 		}...)
// 		require.NoError(t, err)
// 		defer dhtB.Close()

// 		connectNoSync(t, ctx, dhtA, dhtB)

// 		// 我们不期望节点 A 的连接通知能够到达节点 B（反之亦然），因为它们被配置为使用不同的协议 - 但我们还是给它们一个机会。
// 		time.Sleep(time.Second * 2)

// 		err = dhtA.PutValue(ctx, "/v/cat", []byte("meow")) // 在节点 A 存储值 "/v/cat"，值为 "meow"
// 		if err == nil || !strings.Contains(err.Error(), "failed to find any peer in table") {
// 			t.Fatalf("在路由表中不应该能够找到任何对等节点，错误信息为：err:'%v'", err)
// 		}

// 		v, err := dhtB.GetValue(ctx, "/v/cat") // 从节点 B 获取值 "/v/cat"
// 		if v != nil || err != routing.ErrNotFound {
// 			t.Fatalf("get操作应该因为无法找到值而失败，错误信息为: '%v'", err)
// 		}
// 	})
// }

// TestPing 该方法的用途是是测试Ping操作是否成功。
func TestPing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 在测试之前设置DHT节点
	ds := setupDHTS(t, ctx, 2)

	// 将ds[1]的地址添加到ds[0]的Peerstore中
	ds[0].Host().Peerstore().AddAddrs(ds[1].PeerID(), ds[1].Host().Addrs(), peerstore.AddressTTL)

	// 执行Ping操作并断言没有错误发生
	assert.NoError(t, ds[0].Ping(context.Background(), ds[1].PeerID()))
}

// TestClientModeAtInit 该方法的用途是测试在初始化时设置客户端模式是否正确。
func TestClientModeAtInit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置pinger和client两个DHT节点
	pinger := setupDHT(ctx, t, false)
	client := setupDHT(ctx, t, true)

	// 将client的地址添加到pinger的Peerstore中
	pinger.Host().Peerstore().AddAddrs(client.PeerID(), client.Host().Addrs(), peerstore.AddressTTL)

	// 执行Ping操作并断言错误类型为multistream.ErrNotSupported[protocol.ID]
	err := pinger.Ping(context.Background(), client.PeerID())
	assert.True(t, errors.Is(err, multistream.ErrNotSupported[protocol.ID]{}))
}

// TestModeChange 是一个测试函数，用于测试模式切换功能。
func TestModeChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置 clientOnly 和 clientToServer 的 DHT 实例
	clientOnly := setupDHT(ctx, t, true)
	clientToServer := setupDHT(ctx, t, true)

	// 将 clientToServer 的地址添加到 clientOnly 的 Peerstore 中
	clientOnly.Host().Peerstore().AddAddrs(clientToServer.PeerID(), clientToServer.Host().Addrs(), peerstore.AddressTTL)

	// 使用 clientOnly 发送 Ping 请求到 clientToServer
	err := clientOnly.Ping(ctx, clientToServer.PeerID())
	assert.True(t, errors.Is(err, multistream.ErrNotSupported[protocol.ID]{}))

	// 将 clientToServer 的模式设置为 modeServer
	err = clientToServer.setMode(modeServer)
	assert.Nil(t, err)

	// 使用 clientOnly 再次发送 Ping 请求到 clientToServer
	err = clientOnly.Ping(ctx, clientToServer.PeerID())
	assert.Nil(t, err)

	// 将 clientToServer 的模式设置为 modeClient
	err = clientToServer.setMode(modeClient)
	assert.Nil(t, err)

	// 使用 clientOnly 再次发送 Ping 请求到 clientToServer
	err = clientOnly.Ping(ctx, clientToServer.PeerID())
	assert.NotNil(t, err)
}

// TestDynamicModeSwitching 是一个测试函数，用于测试动态模式切换功能。
func TestDynamicModeSwitching(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置 prober 和 node 的 DHT 实例
	prober := setupDHT(ctx, t, true)               // 我们的测试工具
	node := setupDHT(ctx, t, true, Mode(ModeAuto)) // 待测试的节点

	// 将 node 的地址添加到 prober 的 Peerstore 中
	prober.Host().Peerstore().AddAddrs(node.PeerID(), node.Host().Addrs(), peerstore.AddressTTL)

	// 使用 prober 的 Host 进行拨号连接到 node
	if _, err := prober.Host().Network().DialPeer(ctx, node.PeerID()); err != nil {
		t.Fatal(err)
	}

	// 创建一个事件发射器，用于监听本地可达性变化事件
	emitter, err := node.host.EventBus().Emitter(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		t.Fatal(err)
	}

	// 断言 DHT 客户端模式的状态
	assertDHTClient := func() {
		err = prober.Ping(ctx, node.PeerID())
		assert.True(t, errors.Is(err, multistream.ErrNotSupported[protocol.ID]{}))
		if l := len(prober.RoutingTable().ListPeers()); l != 0 {
			t.Errorf("期望路由表长度为 0，实际为 %d", l)
		}
	}

	// 断言 DHT 服务器模式的状态
	assertDHTServer := func() {
		err = prober.Ping(ctx, node.PeerID())
		assert.Nil(t, err)
		// 由于 prober 在节点更新协议时会调用 fixLowPeers，所以节点应该出现在 prober 的路由表中
		if l := len(prober.RoutingTable().ListPeers()); l != 1 {
			t.Errorf("期望路由表长度为 1，实际为 %d", l)
		}
	}

	// 发送本地可达性变化事件，将可达性设置为私网
	err = emitter.Emit(event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPrivate})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	assertDHTClient()

	// 发送本地可达性变化事件，将可达性设置为公网
	err = emitter.Emit(event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	assertDHTServer()

	// 发送本地可达性变化事件，将可达性设置为未知
	err = emitter.Emit(event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityUnknown})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	assertDHTClient()
}

// TestInvalidKeys 是一个测试函数，用于测试无效键的情况。
// func TestInvalidKeys(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	nDHTs := 2
// 	dhts := setupDHTS(t, ctx, nDHTs)
// 	defer func() {
// 		for i := 0; i < nDHTs; i++ {
// 			dhts[i].Close()
// 			defer dhts[i].host.Close()
// 		}
// 	}()

// 	t.Logf("在一个环中连接 %d 个 DHT 实例", nDHTs)
// 	for i := 0; i < nDHTs; i++ {
// 		connect(t, ctx, dhts[i], dhts[(i+1)%len(dhts)])
// 	}

// 	querier := dhts[0]

// 	// 尝试使用空字符串作为键调用 GetClosestPeers 函数，预期应该失败
// 	_, err := querier.GetClosestPeers(ctx, "")
// 	if err == nil {
// 		t.Fatal("获取最近的对等节点应该失败")
// 	}

// 	// 尝试使用空的 CID 调用 FindProviders 函数，预期应该失败
// 	_, err = querier.FindProviders(ctx, cid.Cid{})
// 	switch err {
// 	case routing.ErrNotFound, routing.ErrNotSupported, kb.ErrLookupFailure:
// 		t.Fatal("以错误的错误信息失败：", err)
// 	case nil:
// 		t.Fatal("查找提供者应该失败")
// 	}

// 	// 尝试使用空的 Peer ID 调用 FindPeer 函数，预期应该失败
// 	_, err = querier.FindPeer(ctx, peer.ID(""))
// 	if err != peer.ErrEmptyPeerID {
// 		t.Fatal("预期因为空的 Peer ID 而失败")
// 	}

// 	// 尝试使用空字符串作为键调用 GetValue 函数，预期应该失败
// 	_, err = querier.GetValue(ctx, "")
// 	if err == nil {
// 		t.Fatal("预期应该失败")
// 	}

// 	// 尝试使用空字符串作为键和 "foobar" 作为值调用 PutValue 函数，预期应该失败
// 	err = querier.PutValue(ctx, "", []byte("foobar"))
// 	if err == nil {
// 		t.Fatal("预期应该失败")
// 	}
// }

// // TestV1ProtocolOverride 是一个测试函数，用于测试 V1 协议覆盖功能。
// func TestV1ProtocolOverride(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 创建四个 DHT 实例，其中 d1、d2、d3 使用 V1ProtocolOverride("/myproto") 进行配置
// 	d1 := setupDHT(ctx, t, false, V1ProtocolOverride("/myproto"))
// 	d2 := setupDHT(ctx, t, false, V1ProtocolOverride("/myproto"))
// 	d3 := setupDHT(ctx, t, false, V1ProtocolOverride("/myproto2"))
// 	d4 := setupDHT(ctx, t, false)

// 	dhts := []*DeP2PDHT{d1, d2, d3, d4}

// 	// 将所有实例两两连接起来
// 	for i, dout := range dhts {
// 		for _, din := range dhts[i+1:] {
// 			connectNoSync(t, ctx, dout, din)
// 		}
// 	}

// 	// 等待 d1 和 d2 之间的连接建立
// 	wait(t, ctx, d1, d2)
// 	wait(t, ctx, d2, d1)

// 	time.Sleep(time.Second)

// 	// 验证 d1 和 d2 的路由表中是否只有一个对等节点
// 	if d1.RoutingTable().Size() != 1 || d2.routingTable.Size() != 1 {
// 		t.Fatal("路由表中应该只有一个对等节点")
// 	}

// 	// 验证 d3 和 d4 的路由表是否为空
// 	if d3.RoutingTable().Size() > 0 || d4.RoutingTable().Size() > 0 {
// 		t.Fatal("路由表应该为空")
// 	}
// }

// // TestRoutingFilter 是一个测试函数，用于测试路由过滤器功能。
// func TestRoutingFilter(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	nDHTs := 2
// 	dhts := setupDHTS(t, ctx, nDHTs)
// 	defer func() {
// 		for i := 0; i < nDHTs; i++ {
// 			dhts[i].Close()
// 			defer dhts[i].host.Close()
// 		}
// 	}()

// 	// 将 dhts[0] 的 routingTablePeerFilter 设置为 PublicRoutingTableFilter
// 	dhts[0].routingTablePeerFilter = PublicRoutingTableFilter

// 	// 将 dhts[0] 和 dhts[1] 连接起来
// 	connectNoSync(t, ctx, dhts[0], dhts[1])
// 	wait(t, ctx, dhts[1], dhts[0])

// 	select {
// 	case <-ctx.Done():
// 		// 上下文已完成，测试失败
// 		t.Fatal(ctx.Err())
// 	case <-time.After(time.Millisecond * 200):
// 		// 等待 200 毫秒
// 	}
// }

// TestBootStrapWhenRTIsEmpty 是一个测试函数，用于测试当路由表为空时的引导功能。
// func TestBootStrapWhenRTIsEmpty(t *testing.T) {
// 	if detectrace.WithRace() {
// 		t.Skip("当运行竞态检测器时，跳过依赖时间的测试")
// 	}
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 创建三个引导节点，每个节点连接到另一个节点
// 	nBootStraps := 3
// 	bootstrappers := setupDHTS(t, ctx, nBootStraps)
// 	defer func() {
// 		for i := 0; i < nBootStraps; i++ {
// 			bootstrappers[i].Close()
// 			defer bootstrappers[i].host.Close()
// 		}
// 	}()

// 	bootstrapcons := setupDHTS(t, ctx, nBootStraps)
// 	defer func() {
// 		for i := 0; i < nBootStraps; i++ {
// 			bootstrapcons[i].Close()
// 			defer bootstrapcons[i].host.Close()
// 		}
// 	}()
// 	for i := 0; i < nBootStraps; i++ {
// 		connect(t, ctx, bootstrappers[i], bootstrapcons[i])
// 	}

// 	// 将引导节点的地址转换为 p2p 地址
// 	bootstrapAddrs := make([]peer.AddrInfo, nBootStraps)
// 	for i := 0; i < nBootStraps; i++ {
// 		b := peer.AddrInfo{ID: bootstrappers[i].self, Addrs: bootstrappers[i].host.Addrs()}
// 		bootstrapAddrs[i] = b
// 	}

// 	{
// 		// ----------------
// 		// 我们将初始化一个带有一个引导节点的 DHT，将其连接到另一个 DHT，
// 		// 然后从路由表中移除后者。
// 		// 这应该将引导节点和引导节点所连接的节点添加到路由表中。
// 		// AutoRefresh 需要启用。

// 		h1, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 		require.NoError(t, err)
// 		h1.Start()
// 		dht1, err := New(
// 			ctx,
// 			h1,
// 			testPrefix,
// 			NamespacedValidator("v", blankValidator{}),
// 			Mode(ModeServer),
// 			BootstrapPeers(bootstrapAddrs[0]),
// 		)
// 		require.NoError(t, err)
// 		dht2 := setupDHT(ctx, t, false)
// 		defer func() {
// 			dht1.host.Close()
// 			dht2.host.Close()
// 			dht1.Close()
// 			dht2.Close()
// 		}()
// 		connect(t, ctx, dht1, dht2)
// 		require.NoError(t, dht2.Close())
// 		require.NoError(t, dht2.host.Close())
// 		require.NoError(t, dht1.host.Network().ClosePeer(dht2.self))
// 		dht1.routingTable.RemovePeer(dht2.self)
// 		require.NotContains(t, dht2.self, dht1.routingTable.ListPeers())
// 		require.Eventually(t, func() bool {
// 			return dht1.routingTable.Size() == 2 && dht1.routingTable.Find(bootstrappers[0].self) != "" &&
// 				dht1.routingTable.Find(bootstrapcons[0].self) != ""
// 		}, 5*time.Second, 500*time.Millisecond)
// 	}

// 	{
// 		// ----------------
// 		// 我们将初始化一个带有两个引导节点的 DHT，将其连接到另一个 DHT，
// 		// 然后从另一个 DHT 中移除 DHT 处理程序，这将使第一个 DHT 的路由表为空。
// 		// 这应该将引导节点和引导节点所连接的节点添加到第一个 DHT 的路由表中。
// 		// AutoRefresh 需要启用。

// 		h1, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 		require.NoError(t, err)
// 		h1.Start()
// 		dht1, err := New(
// 			ctx,
// 			h1,
// 			testPrefix,
// 			NamespacedValidator("v", blankValidator{}),
// 			Mode(ModeServer),
// 			BootstrapPeers(bootstrapAddrs[1], bootstrapAddrs[2]),
// 		)
// 		require.NoError(t, err)

// 		dht2 := setupDHT(ctx, t, false)
// 		connect(t, ctx, dht1, dht2)
// 		defer func() {
// 			dht1.host.Close()
// 			dht2.host.Close()
// 			dht1.Close()
// 			dht2.Close()
// 		}()
// 		connect(t, ctx, dht1, dht2)
// 		require.NoError(t, dht2.setMode(modeClient))

// 		require.Eventually(t, func() bool {
// 			rt := dht1.routingTable

// 			return rt.Size() == 4 && rt.Find(bootstrappers[1].self) != "" &&
// 				rt.Find(bootstrappers[2].self) != "" && rt.Find(bootstrapcons[1].self) != "" && rt.Find(bootstrapcons[2].self) != ""
// 		}, 5*time.Second, 500*time.Millisecond)
// 	}
// }

// // TestBootstrapPeersFunc 是一个测试函数，用于测试 BootstrapPeersFunc 函数的功能。
// func TestBootstrapPeersFunc(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()
// 	var lock sync.Mutex

// 	// 定义 bootstrapFuncA，返回一个空的引导节点列表
// 	bootstrapFuncA := func() []peer.AddrInfo {
// 		return []peer.AddrInfo{}
// 	}
// 	dhtA := setupDHT(ctx, t, false, BootstrapPeersFunc(bootstrapFuncA))

// 	// 定义 bootstrapPeersB 和 bootstrapFuncB
// 	bootstrapPeersB := []peer.AddrInfo{}
// 	bootstrapFuncB := func() []peer.AddrInfo {
// 		lock.Lock()
// 		defer lock.Unlock()
// 		return bootstrapPeersB
// 	}

// 	// 使用 bootstrapFuncB 初始化 dhtB
// 	dhtB := setupDHT(ctx, t, false, BootstrapPeersFunc(bootstrapFuncB))
// 	require.Equal(t, 0, len(dhtB.host.Network().Peers()))

// 	// 创建一个 AddrInfo 对象 addrA，包含 dhtA 的信息
// 	addrA := peer.AddrInfo{
// 		ID:    dhtA.self,
// 		Addrs: dhtA.host.Addrs(),
// 	}

// 	lock.Lock()
// 	// 将 addrA 添加到 bootstrapPeersB 中
// 	bootstrapPeersB = []peer.AddrInfo{addrA}
// 	lock.Unlock()

// 	// 调用 dhtB 的 fixLowPeers 方法，用于修复低节点数的问题
// 	dhtB.fixLowPeers()
// 	require.NotEqual(t, 0, len(dhtB.host.Network().Peers()))
// }

// // TestPreconnectedNodes 是一个测试函数，用于测试预连接节点的功能。
// func TestPreconnectedNodes(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	opts := []Option{
// 		testPrefix,
// 		DisableAutoRefresh(),
// 		Mode(ModeServer),
// 	}

// 	// 创建两个主机
// 	h1, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 	require.NoError(t, err)
// 	h1.Start()
// 	defer h1.Close()
// 	h2, err := bhost.NewHost(swarmt.GenSwarm(t, swarmt.OptDisableReuseport), new(bhost.HostOpts))
// 	require.NoError(t, err)
// 	h2.Start()
// 	defer h2.Close()

// 	// 设置第一个 DHT
// 	d1, err := New(ctx, h1, opts...)
// 	require.NoError(t, err)
// 	defer d1.Close()

// 	// 将第一个主机连接到第二个主机
// 	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
// 	require.NoError(t, err)

// 	// 等待识别完成，通过检查支持的协议来确定
// 	// TODO: 这一步是否必要？我们可以使用 h2.Connect(h1) 并等待识别完成吗？
// 	require.Eventually(t, func() bool {
// 		h1Protos, err := h2.Peerstore().SupportsProtocols(h1.ID(), d1.protocols...)
// 		require.NoError(t, err)

// 		return len(h1Protos) > 0
// 	}, 10*time.Second, time.Millisecond)

// 	// 设置第二个 DHT
// 	d2, err := New(ctx, h2, opts...)
// 	require.NoError(t, err)
// 	defer h2.Close()

// 	connect(t, ctx, d1, d2)

// 	// 测试是否正常工作
// 	peers, err := d2.GetClosestPeers(ctx, "testkey")
// 	require.NoError(t, err)

// 	require.Equal(t, len(peers), 1, "为什么有多个对等节点？")
// 	require.Equal(t, h1.ID(), peers[0], "找不到对等节点")
// }

// TestAddrFilter 是一个测试函数，用于测试地址过滤器的功能。
// func TestAddrFilter(t *testing.T) {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	// 生成一组地址
// 	publicAddrs := []ma.Multiaddr{
// 		ma.StringCast("/ip4/1.2.3.1/tcp/123"),
// 		ma.StringCast("/ip4/160.160.160.160/tcp/1600"),
// 		ma.StringCast("/ip6/2001::10/tcp/123"),
// 	}
// 	privAddrs := []ma.Multiaddr{
// 		ma.StringCast("/ip4/192.168.1.100/tcp/123"),
// 		ma.StringCast("/ip4/172.16.10.10/tcp/123"),
// 		ma.StringCast("/ip4/10.10.10.10/tcp/123"),
// 		ma.StringCast("/ip6/fc00::10/tcp/123"),
// 	}
// 	loopbackAddrs := []ma.Multiaddr{
// 		ma.StringCast("/ip4/127.0.0.100/tcp/123"),
// 		ma.StringCast("/ip6/::1/tcp/123"),
// 	}

// 	allAddrs := append(publicAddrs, privAddrs...)
// 	allAddrs = append(allAddrs, loopbackAddrs...)

// 	// 生成不同的地址过滤器
// 	acceptAllFilter := AddressFilter(func(addrs []ma.Multiaddr) []ma.Multiaddr {
// 		return addrs
// 	})
// 	rejectAllFilter := AddressFilter(func(addrs []ma.Multiaddr) []ma.Multiaddr {
// 		return []ma.Multiaddr{}
// 	})
// 	publicIpFilter := AddressFilter(func(addrs []ma.Multiaddr) []ma.Multiaddr {
// 		return ma.FilterAddrs(addrs, manet.IsPublicAddr)
// 	})
// 	localIpFilter := AddressFilter(func(addrs []ma.Multiaddr) []ma.Multiaddr {
// 		return ma.FilterAddrs(addrs, func(a ma.Multiaddr) bool { return !manet.IsIPLoopback(a) })
// 	})

// 	// 为“远程”对等节点生成 peerid
// 	_, pub, err := crypto.GenerateKeyPair(
// 		crypto.Ed25519, // 选择密钥类型，Ed25519 是一个不错的选择
// 		-1,             // 当可能时选择密钥长度（例如 RSA）
// 	)
// 	require.NoError(t, err)
// 	peerid, err := peer.IDFromPublicKey(pub)
// 	require.NoError(t, err)

// 	// DHT 接受所有地址
// 	d0 := setupDHT(ctx, t, false, acceptAllFilter)

// 	// peerstore 应该只包含自身
// 	require.Equal(t, 1, d0.host.Peerstore().Peers().Len())

// 	d0.maybeAddAddrs(peerid, allAddrs, time.Minute)
// 	require.Equal(t, 2, d0.host.Peerstore().Peers().Len())
// 	for _, a := range allAddrs {
// 		// 检查 peerstore 是否包含远程对等节点的所有地址
// 		require.Contains(t, d0.host.Peerstore().Addrs(peerid), a)
// 	}

// 	// DHT 拒绝所有地址
// 	d1 := setupDHT(ctx, t, false, rejectAllFilter)
// 	d1.maybeAddAddrs(peerid, allAddrs, time.Minute)
// 	// peerstore 不应添加远程对等节点（所有地址都被拒绝）
// 	require.Equal(t, 1, d1.host.Peerstore().Peers().Len())

// 	// DHT 只接受公共地址
// 	d2 := setupDHT(ctx, t, false, publicIpFilter)
// 	d2.maybeAddAddrs(peerid, allAddrs, time.Minute)
// 	for _, a := range publicAddrs {
// 		// 检查 peerstore 是否只包含远程对等节点的公共地址
// 		require.Contains(t, d2.host.Peerstore().Addrs(peerid), a)
// 	}
// 	require.Equal(t, len(publicAddrs), len(d2.host.Peerstore().Addrs(peerid)))

// 	// DHT 只接受非回环地址
// 	d3 := setupDHT(ctx, t, false, localIpFilter)
// 	d3.maybeAddAddrs(peerid, allAddrs, time.Minute)
// 	for _, a := range publicAddrs {
// 		// 检查 peerstore 是否只包含远程对等节点的非回环地址
// 		require.Contains(t, d3.host.Peerstore().Addrs(peerid), a)
// 	}
// 	for _, a := range privAddrs {
// 		// 检查 peerstore 是否只包含远程对等节点的非回环地址
// 		require.Contains(t, d3.host.Peerstore().Addrs(peerid), a)
// 	}
// 	require.Equal(t, len(publicAddrs)+len(privAddrs), len(d3.host.Peerstore().Addrs(peerid)))
// }
