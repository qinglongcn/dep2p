package keyspace

import (
	"sort"

	"math/big"
)

// Key 表示 KeySpace 中的标识符。它持有与之关联的 KeySpace 的引用，以及原始标识符和新的 KeySpace 字节。
type Key struct {

	// Space 是与此 Key 相关联的 KeySpace。
	Space KeySpace

	// Original 是标识符的原始值。
	Original []byte

	// Bytes 是标识符在 KeySpace 中的新值。
	Bytes []byte
}

// Equal 返回此 Key 是否与另一个 Key 相等。
func (k1 Key) Equal(k2 Key) bool {
	if k1.Space != k2.Space {
		panic("k1 和 k2 不在同一个 KeySpace 中。")
	}
	return k1.Space.Equal(k1, k2)
}

// Less 返回此 Key 是否在另一个 Key 之前。
func (k1 Key) Less(k2 Key) bool {
	if k1.Space != k2.Space {
		panic("k1 和 k2 不在同一个 KeySpace 中。")
	}
	return k1.Space.Less(k1, k2)
}

// Distance 返回此 Key 到另一个 Key 的距离。
func (k1 Key) Distance(k2 Key) *big.Int {
	if k1.Space != k2.Space {
		panic("k1 和 k2 不在同一个 KeySpace 中。")
	}
	return k1.Space.Distance(k1, k2)
}

// KeySpace 是用于在标识符上执行数学运算的对象。每个 KeySpace 都有自己的属性和规则。参见 XorKeySpace。
type KeySpace interface {

	// Key 将标识符转换为此空间中的 Key。
	Key([]byte) Key

	// Equal 返回在此 KeySpace 中两个 Key 是否相等。
	Equal(Key, Key) bool

	// Distance 返回在此 KeySpace 中的距离度量。
	Distance(Key, Key) *big.Int

	// Less 返回第一个 Key 是否小于第二个 Key。
	Less(Key, Key) bool
}

// byDistanceToCenter 是一个用于按与中心的接近程度对 Keys 进行排序的类型。
type byDistanceToCenter struct {
	Center Key   // 中心 Key
	Keys   []Key // Key 列表
}

// Len 返回 Keys 的长度。
func (s byDistanceToCenter) Len() int {
	return len(s.Keys)
}

// Swap 交换 Keys 中的两个元素的位置。
// 通过使用多重赋值操作，将第 i 个键与第 j 个键进行交换。
func (s byDistanceToCenter) Swap(i, j int) {
	s.Keys[i], s.Keys[j] = s.Keys[j], s.Keys[i] // 通过多重赋值操作交换第 i 个键和第 j 个键的位置
}

// Less 比较 Keys 中的两个元素的距离，判断是否前者距离中心更近。
// 使用 Center 的 Distance 方法计算距离，并使用 Cmp 方法进行比较。
// 如果 a 小于 b，则返回 true。
func (s byDistanceToCenter) Less(i, j int) bool {
	a := s.Center.Distance(s.Keys[i]) // 计算第 i 个键与中心键的距离
	b := s.Center.Distance(s.Keys[j]) // 计算第 j 个键与中心键的距离
	return a.Cmp(b) == -1             // 如果 a 小于 b，则返回 true
}

// SortByDistance 接受一个 KeySpace、一个中心 Key 和一个要排序的 Key 列表 toSort。
// 它返回一个新的列表，其中 toSort 的 Key 按照与中心 Key 的距离进行排序。
func SortByDistance(sp KeySpace, center Key, toSort []Key) []Key {
	toSortCopy := make([]Key, len(toSort)) // 创建一个 toSort 的副本
	copy(toSortCopy, toSort)               // 复制 toSort 的内容到副本中
	bdtc := &byDistanceToCenter{           // 创建一个 byDistanceToCenter 结构体实例
		Center: center,     // 设置中心 Key
		Keys:   toSortCopy, // 设置要排序的 Key 列表，使用副本
	}
	sort.Sort(bdtc)  // 使用 sort.Sort 方法对 Keys 进行排序
	return bdtc.Keys // 返回排序后的 Keys 列表
}
