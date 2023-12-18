package kbucket

import (
	"container/list"
	"sort"

	"github.com/libp2p/go-libp2p/core/peer"
)

// peerDistance 是一个辅助结构，用于按照与本地节点的距离对对等节点进行排序
type peerDistance struct {
	p        peer.ID // p 字段表示对等节点的 ID。
	mode     int     // 目标节点的运行模式
	distance ID      // distance 字段表示对等节点与本地节点的距离。
}

// peerDistanceSorter 实现 sort.Interface 接口，用于按照异或距离对对等节点进行排序
type peerDistanceSorter struct {
	peers  []peerDistance // peers 字段是一个 peerDistance 类型的切片，表示待排序的对等节点列表。
	target ID             // target 字段是一个 ID 类型，表示排序的目标，即与目标距离最近的节点将排在前面。
}

// Len 返回 peerDistanceSorter 中的节点数量
func (pds *peerDistanceSorter) Len() int { return len(pds.peers) }

// Swap 交换 peerDistanceSorter 中两个位置的节点
func (pds *peerDistanceSorter) Swap(a, b int) {
	pds.peers[a], pds.peers[b] = pds.peers[b], pds.peers[a]
}

// Less 比较 peerDistanceSorter 中两个位置的节点的距离大小
func (pds *peerDistanceSorter) Less(a, b int) bool {
	return pds.peers[a].distance.less(pds.peers[b].distance)
}

// appendPeer 将 peer.ID 添加到排序器的切片中，可能会导致切片不再有序。
func (pds *peerDistanceSorter) appendPeer(p peer.ID, pDhtId ID) {
	pds.peers = append(pds.peers, peerDistance{
		p:        p,
		distance: xor(pds.target, pDhtId),
	})
}

// appendPeersFromList 将列表中的 peer.ID 值添加到排序器的切片中，可能会导致切片不再有序。
func (pds *peerDistanceSorter) appendPeersFromList(l *list.List) {
	for e := l.Front(); e != nil; e = e.Next() {
		pds.appendPeer(e.Value.(*PeerInfo).Id, e.Value.(*PeerInfo).dhtId)
	}
}

// sort 对 peerDistanceSorter 进行排序
func (pds *peerDistanceSorter) sort() {
	sort.Sort(pds)
}

// SortClosestPeers 按照与目标节点的升序距离对给定的对等节点进行排序。返回一个新的切片。
func SortClosestPeers(peers []peer.ID, target ID) []peer.ID {
	// 创建一个排序器
	sorter := peerDistanceSorter{
		peers:  make([]peerDistance, 0, len(peers)),
		target: target,
	}

	// 遍历对等节点列表，将节点信息添加到排序器中
	for _, p := range peers {
		sorter.appendPeer(p, ConvertPeerID(p))
	}

	// 对排序器进行排序
	sorter.sort()

	// 创建一个切片用于存储排序后的节点列表
	out := make([]peer.ID, 0, sorter.Len())

	// 遍历排序器中的节点信息，将节点标识添加到输出切片中
	for _, p := range sorter.peers {
		out = append(out, p.p)
	}

	// 返回排序后的节点列表
	return out
}
