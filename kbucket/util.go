package kbucket

import (
	"fmt"

	"github.com/bpfs/dep2p/kbucket/keyspace"

	"github.com/minio/sha256-simd"

	"github.com/libp2p/go-libp2p/core/peer"

	u "github.com/ipfs/boxo/util"
)

// ErrLookupFailure 表示路由表查询未返回任何结果时的错误。这不是预期行为。
var ErrLookupFailure = fmt.Errorf("failed to find any peer in table")

// ID 是一个在 XORKeySpace 中的 DeP2PDHT ID 的类型
//
// 类型 dht.ID 表示其内容已从 peer.ID 或 util.Key 进行了哈希处理。这统一了键空间。
type ID []byte

// less 比较两个 ID 的大小，返回是否 a < b
func (id ID) less(other ID) bool {
	// 将当前 ID 转换为 XORKeySpace 中的 keyspace.Key 类型
	a := keyspace.Key{Space: keyspace.XORKeySpace, Bytes: id}
	// 将另一个 ID 转换为 XORKeySpace 中的 keyspace.Key 类型
	b := keyspace.Key{Space: keyspace.XORKeySpace, Bytes: other}
	// 调用 keyspace.Key 的 Less() 方法比较两个键的大小
	return a.Less(b)
}

// xor 对两个 ID 进行异或运算
func xor(a, b ID) ID {
	return ID(u.XOR(a, b))
}

// CommonPrefixLen 计算两个 ID 的公共前缀长度
func CommonPrefixLen(a, b ID) int {
	return keyspace.ZeroPrefixLen(u.XOR(a, b))
}

// ConvertPeerID 通过哈希处理 Peer ID（Multihash）创建一个 DHT ID
func ConvertPeerID(id peer.ID) ID {
	hash := sha256.Sum256([]byte(id))
	return hash[:]
}

// ConvertKey 通过哈希处理本地键（字符串）创建一个 DHT ID
func ConvertKey(id string) ID {
	hash := sha256.Sum256([]byte(id))
	return hash[:]
}

// Closer 如果节点 a 比节点 b 更接近键 key，则返回 true
func Closer(a, b peer.ID, key string) bool {
	// 将节点 a 的 Peer ID 转换为 DHT ID
	aid := ConvertPeerID(a)
	// 将节点 b 的 Peer ID 转换为 DHT ID
	bid := ConvertPeerID(b)
	// 将键 key 转换为 DHT ID
	tgt := ConvertKey(key)
	// 计算节点 a 与键 key 的距离
	adist := xor(aid, tgt)
	// 计算节点 b 与键 key 的距离
	bdist := xor(bid, tgt)

	// 判断节点 a 与键 key 的距离是否比节点 b 与键 key 的距离更近
	return adist.less(bdist)
}
