package kbucket

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	mh "github.com/multiformats/go-multihash"
)

// maxCplForRefresh 是我们支持的刷新的最大 cpl。
// 这个限制存在是因为目前我们只能生成 'maxCplForRefresh' 位的前缀。
const maxCplForRefresh uint = 15

// GetTrackedCplsForRefresh 返回我们正在追踪的用于刷新的 Cpl。
// 调用者可以自由地修改返回的切片，因为它是一个防御性副本。
func (rt *RoutingTable) GetTrackedCplsForRefresh() []time.Time {
	// 获取最大公共前缀
	maxCommonPrefix := rt.maxCommonPrefix()
	if maxCommonPrefix > maxCplForRefresh {
		maxCommonPrefix = maxCplForRefresh
	}

	// 读取锁，确保并发安全
	rt.cplRefreshLk.RLock()
	defer rt.cplRefreshLk.RUnlock()

	// 创建一个切片用于存储时间戳，切片的长度为 maxCommonPrefix+1
	cpls := make([]time.Time, maxCommonPrefix+1)
	for i := uint(0); i <= maxCommonPrefix; i++ {
		// 如果还没有刷新过，则默认为零值
		cpls[i] = rt.cplRefreshedAt[i]
	}
	return cpls
}

// randUint16 生成一个随机的 uint16 值
func randUint16() (uint16, error) {
	// 读取一个随机前缀
	var prefixBytes [2]byte
	_, err := rand.Read(prefixBytes[:])
	return binary.BigEndian.Uint16(prefixBytes[:]), err
}

// GenRandPeerID 为给定的 Cpl 生成一个随机的 peerID
func (rt *RoutingTable) GenRandPeerID(targetCpl uint) (peer.ID, error) {
	if targetCpl > maxCplForRefresh {
		return "", fmt.Errorf("无法为大于 %d 的 Cpl 生成 peer ID", maxCplForRefresh)
	}

	// 将本地前缀转换为 uint16
	localPrefix := binary.BigEndian.Uint16(rt.local)

	// 对于 ID 为 `L` 的主机，ID 为 `K` 的节点仅属于 ID 为 `B` 的桶，当且仅当 CommonPrefixLen(L,K) 等于 B。
	// 因此，要实现目标前缀 `T`，我们必须切换 L 中的第 (T+1) 位，然后从 L 中复制 (T+1) 位到我们随机生成的前缀中。
	toggledLocalPrefix := localPrefix ^ (uint16(0x8000) >> targetCpl)
	randPrefix, err := randUint16()
	if err != nil {
		return "", err
	}

	// 将切换后的本地前缀和随机位组合在一起，正确的偏移量上
	// 以使得仅有前 `targetCpl` 位与本地 ID 匹配。
	mask := (^uint16(0)) << (16 - (targetCpl + 1))
	targetPrefix := (toggledLocalPrefix & mask) | (randPrefix & ^mask)

	// 转换为已知的 peer ID。
	key := keyPrefixMap[targetPrefix]
	id := [32 + 2]byte{mh.SHA2_256, 32}
	binary.BigEndian.PutUint32(id[2:], key)
	return peer.ID(id[:]), nil
}

// GenRandomKey 根据提供的公共前缀长度（Cpl）生成一个匹配的随机键。
// 返回的键的前 targetCpl 位与本地键的前缀匹配，接下来的一位是本地键在位置 targetCpl+1 处的位的反转，
// 剩余的位是随机生成的。
func (rt *RoutingTable) GenRandomKey(targetCpl uint) (ID, error) {
	// 检查 targetCpl+1 是否大于等于本地键的长度（以字节为单位）
	if int(targetCpl+1) >= len(rt.local)*8 {
		return nil, fmt.Errorf("无法为大于键长度的 Cpl 生成键")
	}
	partialOffset := targetCpl / 8

	// output 包含本地键的前 partialOffset 个字节，其余字节是随机生成的
	output := make([]byte, len(rt.local))
	copy(output, rt.local[:partialOffset])
	_, err := rand.Read(output[partialOffset:])
	if err != nil {
		return nil, err
	}

	remainingBits := 8 - targetCpl%8
	orig := rt.local[partialOffset]

	origMask := ^uint8(0) << remainingBits
	randMask := ^origMask >> 1
	flippedBitOffset := remainingBits - 1
	flippedBitMask := uint8(1) << flippedBitOffset

	// 恢复 orig 的 remainingBits 个最高有效位（Most Significant Bits），
	// 并翻转 orig 的第 flippedBitOffset 位
	output[partialOffset] = orig&origMask | (orig & flippedBitMask) ^ flippedBitMask | output[partialOffset]&randMask

	return ID(output), nil
}

// ResetCplRefreshedAtForID 重置给定 ID 的 Cpl 的刷新时间。
func (rt *RoutingTable) ResetCplRefreshedAtForID(id ID, newTime time.Time) {
	// 计算给定 ID 与本地键的 Cpl（公共前缀长度）
	cpl := CommonPrefixLen(id, rt.local)
	// 如果 Cpl 大于最大可刷新的 Cpl，则直接返回
	if uint(cpl) > maxCplForRefresh {
		return
	}

	rt.cplRefreshLk.Lock()
	defer rt.cplRefreshLk.Unlock()

	// 将给定 Cpl 对应的刷新时间重置为新的时间
	rt.cplRefreshedAt[uint(cpl)] = newTime
}
