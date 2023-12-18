package keyspace

import (
	"bytes"
	"math/big"
	"math/bits"

	u "github.com/ipfs/boxo/util"
	sha256 "github.com/minio/sha256-simd"
)

// XORKeySpace 是一个键空间（KeySpace），具有以下特性：
// - 使用加密哈希（sha256）对标识符进行规范化
// - 使用异或（XOR）操作计算键之间的距离

var XORKeySpace = &xorKeySpace{}
var _ KeySpace = XORKeySpace // 确保符合接口要求

type xorKeySpace struct{}

// Key 将标识符转换为此空间中的键（Key）。
func (s *xorKeySpace) Key(id []byte) Key {
	hash := sha256.Sum256(id) // 使用 sha256 加密哈希算法对标识符进行哈希计算
	key := hash[:]            // 将哈希结果转换为字节切片
	return Key{
		Space:    s,
		Original: id,
		Bytes:    key,
	}
}

// Equal 判断在此键空间中两个键是否相等。
func (s *xorKeySpace) Equal(k1, k2 Key) bool {
	return bytes.Equal(k1.Bytes, k2.Bytes) // 比较两个键的字节切片是否相等
}

// Distance 返回在此键空间中计算的距离度量。
func (s *xorKeySpace) Distance(k1, k2 Key) *big.Int {
	// 对键进行异或操作
	k3 := u.XOR(k1.Bytes, k2.Bytes)

	// 将结果解释为一个整数
	dist := big.NewInt(0).SetBytes(k3)
	return dist
}

// Less 判断第一个键是否小于第二个键。
func (s *xorKeySpace) Less(k1, k2 Key) bool {
	return bytes.Compare(k1.Bytes, k2.Bytes) < 0 // 比较两个键的字节切片的大小
}

// ZeroPrefixLen 返回字节切片中连续零的数量。
func ZeroPrefixLen(id []byte) int {
	for i, b := range id {
		if b != 0 {
			return i*8 + bits.LeadingZeros8(uint8(b)) // 返回连续零的数量
		}
	}
	return len(id) * 8 // 如果整个字节切片都是零，则返回字节切片的总位数
}
