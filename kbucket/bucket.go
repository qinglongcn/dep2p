//go:generate go run ./generate

package kbucket

import (
	"container/list"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerInfo 包含了 K-Bucket 中一个对等节点的所有相关信息。
type PeerInfo struct {
	Id                            peer.ID   // 对等节点的 ID
	Mode                          int       // 目标节点的运行模式
	LastUsefulAt                  time.Time // 对等节点上次对我们有用的时间点（请参阅 DHT 文档以了解有用性的定义）
	LastSuccessfulOutboundQueryAt time.Time // 我们最后一次从对等节点获得成功的查询响应的时间点
	AddedAt                       time.Time // 将此对等节点添加到路由表的时间点
	dhtId                         ID        // 对等节点在 DHT XOR keyspace 中的 ID
	replaceable                   bool      // 如果一个桶已满，此对等节点可以被替换以为新对等节点腾出空间
}

// bucket 是一个对等节点列表。
// 我们在路由表的锁上同步所有对 bucket 的访问，
// 因此在 bucket 中不需要任何锁。
// 如果将来我们想要/需要避免在访问 bucket 时锁定表，
// 调用者将有责任同步对 bucket 的所有访问。
type bucket struct {
	list *list.List // list 字段是一个 list.List 类型，用于存储对等节点。
}

// newBucket 创建一个新的 bucket。
func newBucket() *bucket {
	// 创建一个新的 bucket 实例 b。
	b := new(bucket)
	// 使用 list.New() 创建一个新的链表，并将其赋值给 b.list 字段。
	b.list = list.New()
	// 返回 b 的指针作为结果。
	return b
}

// peers 返回桶中的所有对等节点。
// 调用者可以安全地修改返回的对象，因为这是一个防御性拷贝。
func (b *bucket) peers() []PeerInfo {
	ps := make([]PeerInfo, 0, b.len())                // 创建一个空的 PeerInfo 切片，容量为桶中对等节点的数量
	for e := b.list.Front(); e != nil; e = e.Next() { // 遍历桶中的链表
		p := e.Value.(*PeerInfo) // 获取链表节点的值，并将其转换为 *PeerInfo 类型
		ps = append(ps, *p)      // 将 *PeerInfo 类型的值添加到切片 ps 中
	}
	return ps // 返回包含所有对等节点的切片 ps
}

// min 根据传入的 `lessThan` 比较器，返回桶中的“最小”对等节点。
// 比较器不安全地修改给定的 `PeerInfo`，因为我们传递的是指针。
// 修改返回的值是不安全的。
func (b *bucket) min(lessThan func(p1 *PeerInfo, p2 *PeerInfo) bool) *PeerInfo {
	if b.list.Len() == 0 { // 如果桶为空，则返回 nil
		return nil
	}

	minVal := b.list.Front().Value.(*PeerInfo) // 将链表的第一个节点的值作为初始的最小值

	for e := b.list.Front().Next(); e != nil; e = e.Next() { // 从链表的第二个节点开始遍历
		val := e.Value.(*PeerInfo) // 获取链表节点的值，并将其转换为 *PeerInfo 类型

		if lessThan(val, minVal) { // 使用传入的比较器比较 val 和 minVal
			minVal = val // 如果 val 小于 minVal，则更新最小值为 val
		}
	}

	return minVal // 返回最小的对等节点
}

// updateAllWith 使用给定的更新函数更新桶中的所有对等节点。
func (b *bucket) updateAllWith(updateFnc func(p *PeerInfo)) {
	for e := b.list.Front(); e != nil; e = e.Next() { // 遍历桶中的链表
		val := e.Value.(*PeerInfo) // 获取链表节点的值，并将其转换为 *PeerInfo 类型
		updateFnc(val)             // 调用传入的更新函数，将当前节点的值作为参数传递给更新函数
	}
}

// peerIds 返回桶中所有对等节点的 ID。
func (b *bucket) peerIds() []peer.ID {
	ps := make([]peer.ID, 0, b.list.Len()) // 创建一个切片用于存储对等节点的 ID，初始容量为桶中节点的数量

	for e := b.list.Front(); e != nil; e = e.Next() { // 遍历桶中的链表
		p := e.Value.(*PeerInfo) // 获取链表节点的值，并将其转换为 *PeerInfo 类型
		ps = append(ps, p.Id)    // 将当前节点的 ID 添加到切片中
	}

	return ps // 返回包含所有对等节点 ID 的切片
}

// getPeer 根据给定的 ID 返回对应的对等节点（如果存在）。
// 如果找不到对应的对等节点，则返回 nil。
func (b *bucket) getPeer(p peer.ID) *PeerInfo {
	for e := b.list.Front(); e != nil; e = e.Next() { // 遍历桶中的链表
		if e.Value.(*PeerInfo).Id == p { // 检查当前节点的 ID 是否与给定的 ID 相匹配
			return e.Value.(*PeerInfo) // 如果匹配，返回当前节点的值（*PeerInfo 类型）
		}
	}
	return nil // 如果未找到匹配的对等节点，则返回 nil
}

// remove 从桶中移除具有给定 ID 的对等节点。
// 如果成功移除，则返回 true；否则返回 false。
func (b *bucket) remove(id peer.ID) bool {
	for e := b.list.Front(); e != nil; e = e.Next() { // 遍历桶中的链表
		if e.Value.(*PeerInfo).Id == id { // 检查当前节点的 ID 是否与给定的 ID 相匹配
			b.list.Remove(e) // 如果匹配，从链表中移除当前节点
			return true      // 返回移除成功
		}
	}
	return false // 如果未找到匹配的对等节点，则返回移除失败
}

// pushFront 将给定的对等节点 p 插入到桶的链表头部。
func (b *bucket) pushFront(p *PeerInfo) {
	b.list.PushFront(p) // 将 p 插入到链表的头部
}

// len 返回桶中链表的长度。
func (b *bucket) len() int {
	return b.list.Len() // 返回链表的长度
}

// split 将桶中的对等节点分割成两个桶。方法的接收器将包含 CPL（共同前缀长度）等于 cpl 的对等节点，
// 返回的桶将包含 CPL 大于 cpl 的对等节点（返回的桶中的对等节点更接近目标节点）。
func (b *bucket) split(cpl int, target ID) *bucket {
	out := list.New()      // 创建一个新的链表用于存储分割出的对等节点
	newbuck := newBucket() // 创建一个新的桶
	newbuck.list = out     // 将新桶的链表设置为新创建的链表
	e := b.list.Front()    // 获取桶中链表的第一个节点
	for e != nil {
		pDhtId := e.Value.(*PeerInfo).dhtId        // 获取当前节点的 DHT ID
		peerCPL := CommonPrefixLen(pDhtId, target) // 计算当前节点的 DHT ID 与目标节点的共同前缀长度
		if peerCPL > cpl {                         // 如果共同前缀长度大于 cpl
			cur := e              // 保存当前节点的引用
			out.PushBack(e.Value) // 将当前节点添加到新链表中
			e = e.Next()          // 移动到下一个节点
			b.list.Remove(cur)    // 从原链表中移除当前节点
			continue              // 继续处理下一个节点
		}
		e = e.Next() // 移动到下一个节点
	}
	return newbuck // 返回新的桶
}

// maxCommonPrefix 返回桶中任何对等节点与目标节点的最大共同前缀长度。
func (b *bucket) maxCommonPrefix(target ID) uint {
	maxCpl := uint(0)                                 // 初始化最大共同前缀长度为 0
	for e := b.list.Front(); e != nil; e = e.Next() { // 遍历桶中的链表节点
		cpl := uint(CommonPrefixLen(e.Value.(*PeerInfo).dhtId, target)) // 计算当前节点的 DHT ID 与目标节点的共同前缀长度
		if cpl > maxCpl {                                               // 如果当前共同前缀长度大于最大共同前缀长度
			maxCpl = cpl // 更新最大共同前缀长度
		}
	}
	return maxCpl // 返回最大共同前缀长度
}
