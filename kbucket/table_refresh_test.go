package kbucket

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	pstore "github.com/libp2p/go-libp2p/p2p/host/peerstore"

	"github.com/stretchr/testify/require"
)

// TestGenRandPeerID 是用于测试 GenRandPeerID 函数的函数
func TestGenRandPeerID(t *testing.T) {
	t.Parallel()

	local := test.RandPeerIDFatal(t) // 生成一个随机的本地 PeerID
	m := pstore.NewMetrics()         // 创建一个新的存储度量指标的实例
	rt, err := NewRoutingTable(1, ConvertPeerID(local), time.Hour, m, NoOpThreshold, nil)
	require.NoError(t, err)

	// 生成超过 maxCplForRefresh 的 PeerID 失败
	p, err := rt.GenRandPeerID(maxCplForRefresh + 1)
	require.Error(t, err)
	require.Empty(t, p)

	// 测试生成随机 Peer ID
	for cpl := uint(0); cpl <= maxCplForRefresh; cpl++ {
		peerID, err := rt.GenRandPeerID(cpl)
		require.NoError(t, err)

		require.True(t, uint(CommonPrefixLen(ConvertPeerID(peerID), rt.local)) == cpl, "failed for cpl=%d", cpl)
	}
}

// TestGenRandomKey 是用于测试 GenRandomKey 函数的函数
func TestGenRandomKey(t *testing.T) {
	// 可以并行运行的测试
	t.Parallel()

	// 运行多次以确保测试不仅仅是侥幸
	for i := 0; i < 100; i++ {
		// 生成具有随机本地 PeerID 的路由表
		local := test.RandPeerIDFatal(t) // 生成一个随机的本地 PeerID
		m := pstore.NewMetrics()         // 创建一个新的存储度量指标的实例
		rt, err := NewRoutingTable(1, ConvertPeerID(local), time.Hour, m, NoOpThreshold, nil)
		require.NoError(t, err)

		// cpl >= 256 时 GenRandomKey 失败
		_, err = rt.GenRandomKey(256)
		require.Error(t, err)
		_, err = rt.GenRandomKey(300)
		require.Error(t, err)

		// 比特位比较说明：
		// O 表示相同的比特位，X 表示不同的比特位，? 表示不关心的比特位

		// 我们将生成的密钥与本地密钥进行比较
		// 对于 CPL = X，前 X 位应相同，第 X+1 位应不同，其余位应是随机的/不关心的

		// cpl = 0 时，应返回不同的第一位
		// X??????? ???...
		key0, err := rt.GenRandomKey(0)
		require.NoError(t, err)
		// 最高有效位应不同
		require.NotEqual(t, key0[0]>>7, rt.local[0]>>7)

		// cpl = 1 时，应返回不同的第二位
		// OX?????? ???...
		key1, err := rt.GenRandomKey(1)
		require.NoError(t, err)
		// 最高有效位应相同，因为 cpl = 1
		require.Equal(t, key1[0]>>7, rt.local[0]>>7)
		// 第二高有效位应不同
		require.NotEqual(t, (key1[0]<<1)>>6, (rt.local[0]<<1)>>6)

		// cpl = 2 时，应返回不同的第三位
		// OOX????? ???...
		key2, err := rt.GenRandomKey(2)
		require.NoError(t, err)
		// 前两个最高有效位应相同，因为 cpl = 2
		require.Equal(t, key2[0]>>6, rt.local[0]>>6)
		// 第三个最高有效位应不同
		require.NotEqual(t, (key2[0]<<2)>>5, (rt.local[0]<<2)>>5)

		// cpl = 7 时，应返回不同的第八位
		// OOOOOOOX ???...
		key7, err := rt.GenRandomKey(7)
		require.NoError(t, err)
		// 前七个最高有效位应相同，因为 cpl = 7
		require.Equal(t, key7[0]>>1, rt.local[0]>>1)
		// 第八位应不同
		require.NotEqual(t, key7[0]<<7, rt.local[0]<<7)

		// cpl = 8 时，应返回不同的第九位
		// OOOOOOOO X???...
		key8, err := rt.GenRandomKey(8)
		require.NoError(t, err)
		// 前八个最高有效位应相同，因为 cpl = 8
		require.Equal(t, key8[0], rt.local[0])
		// 第九位应不同
		require.NotEqual(t, key8[1]>>7, rt.local[1]>>7)

		// cpl = 53 时，应返回不同的第 54 位
		// OOOOOOOO OOOOOOOO OOOOOOOO OOOOOOOO OOOOOOOO OOOOOOOO OOOOOX?? ???...
		key53, err := rt.GenRandomKey(53)
		require.NoError(t, err)
		// 前 53 个最高有效位应相同，因为 cpl = 53
		require.Equal(t, key53[:6], rt.local[:6])
		require.Equal(t, key53[6]>>3, rt.local[6]>>3)
		// 第 54 位应不同
		require.NotEqual(t, (key53[6]<<5)>>7, (rt.local[6]<<5)>>7)
	}
}

// TestRefreshAndGetTrackedCpls 是用于测试 RefreshAndGetTrackedCpls 函数的函数
func TestRefreshAndGetTrackedCpls(t *testing.T) {
	t.Parallel()

	const (
		minCpl  = 8  // 最小 Cpl 值
		testCpl = 10 // 测试用的 Cpl 值
		maxCpl  = 12 // 最大 Cpl 值
	)

	local := test.RandPeerIDFatal(t) // 生成一个随机的本地 PeerID
	m := pstore.NewMetrics()         // 创建一个新的存储度量指标的实例
	rt, err := NewRoutingTable(2, ConvertPeerID(local), time.Hour, m, NoOpThreshold, nil)
	require.NoError(t, err)

	// 获取跟踪的 Cpl 值
	trackedCpls := rt.GetTrackedCplsForRefresh()
	// 应该没有任何值
	require.Len(t, trackedCpls, 1)

	var peerIDs []peer.ID
	for i := minCpl; i <= maxCpl; i++ {
		id, err := rt.GenRandPeerID(uint(i))
		require.NoError(t, err)
		peerIDs = append(peerIDs, id)
	}

	// 添加 Peer ID
	for i, id := range peerIDs {
		added, err := rt.TryAddPeer(id, 0, true, false)
		require.NoError(t, err)
		require.True(t, added)
		require.Len(t, rt.GetTrackedCplsForRefresh(), minCpl+i+1)
	}

	// 逐渐移除，直到测试用的 Cpl 值
	for i := maxCpl; i > testCpl; i-- {
		rt.RemovePeer(peerIDs[i-minCpl])
		require.Len(t, rt.GetTrackedCplsForRefresh(), i)
	}

	// 应该跟踪测试用的 Cpl 值
	trackedCpls = rt.GetTrackedCplsForRefresh()
	require.Len(t, trackedCpls, testCpl+1)
	// 所有值应该为零
	for _, refresh := range trackedCpls {
		require.True(t, refresh.IsZero(), "跟踪的 Cpl 值应为零")
	}

	// 添加本地 Peer ID 来填满路由表
	added, err := rt.TryAddPeer(local, 0, true, false)
	require.NoError(t, err)
	require.True(t, added)

	// 应该跟踪最大的 Cpl 值
	trackedCpls = rt.GetTrackedCplsForRefresh()
	require.Len(t, trackedCpls, int(maxCplForRefresh)+1)

	// 不应刷新
	for _, refresh := range trackedCpls {
		require.True(t, refresh.IsZero(), "跟踪的 Cpl 值应为零")
	}

	now := time.Now()
	// 重置测试用的 Peer ID 的刷新时间
	rt.ResetCplRefreshedAtForID(ConvertPeerID(peerIDs[testCpl-minCpl]), now)

	// 仍然应该跟踪所有的桶
	trackedCpls = rt.GetTrackedCplsForRefresh()
	require.Len(t, trackedCpls, int(maxCplForRefresh)+1)

	for i, refresh := range trackedCpls {
		if i == testCpl {
			require.True(t, now.Equal(refresh), "测试用的 Cpl 值应该有正确的刷新时间")
		} else {
			require.True(t, refresh.IsZero(), "其他 Cpl 值应为零")
		}
	}
}
