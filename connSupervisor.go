package dep2p

import (
	"context"
	"sync"
	"time"

	"github.com/bpfs/dep2p/kbucket"
	"github.com/bpfs/dep2p/utils"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/sirupsen/logrus"
)

const (
	// DefaultTryTimes 是默认的尝试次数。最大超时时间为10分钟10秒。
	DefaultTryTimes = 15
	// DefaultTryTimesAfterMaxTime 是最大超时时间（90天）后的默认尝试次数。
	DefaultTryTimesAfterMaxTime = 6 * 24 * 90
)

// ConnSupervisor 是一个连接监管器。
type ConnSupervisor struct {
	ctx               context.Context                // 上下文
	host              host.Host                      // LibP2p 主机
	peerDHT           *DeP2PDHT                       // 分布式哈希表
	nodeInfo          *NodeInfo                      // 节点信息
	startUp           bool                           // 启动状态
	peerAddrInfos     []peer.AddrInfo                // 对等节点地址信息
	peerAddrInfosLock sync.RWMutex                   // 对等节点地址信息锁
	signal            bool                           // 信号
	signalLock        sync.RWMutex                   // 信号锁
	tryConnectLock    sync.Mutex                     // 尝试连接锁
	IsConnected       bool                           // 是否有已连接到对等节点
	cmLock            sync.RWMutex                   // 用于控制对此结构体的并发读写访问
	actuators         map[peer.ID]*tryToDialActuator // 尝试拨号执行器映射
	routingTables     map[int]*kbucket.RoutingTable  // k桶信息
}

type tryToDialActuator struct {
	peerInfo  peer.AddrInfo // 对等节点地址信息
	nodeInfo  *NodeInfo     // 节点信息
	mode      int           // 节点类型
	connected bool          // 当前对等节点是否已连接
	fibonacci []int64       // 斐波那契数列，用于计算重试连接的时间间隔
	idx       int           // 斐波那契数列的索引
	giveUp    bool          // 是否放弃连接的标志
	finish    bool          // 是否完成连接的标志
	statC     chan struct{} // 状态通道，用于同步连接状态
	tryTimes  int           // 尝试次数
}

// newTryToDialActuator 创建一个新的尝试拨号行为器实例
func newTryToDialActuator(peerInfo peer.AddrInfo, connected bool, tryTimes, mode int, nodeInfo *NodeInfo) *tryToDialActuator {
	return &tryToDialActuator{
		peerInfo:  peerInfo, // 设置对等节点地址信息
		nodeInfo:  nodeInfo,
		mode:      mode,
		fibonacci: utils.FibonacciArray(tryTimes), // 生成斐波那契数列
		idx:       0,                              // 初始化斐波那契数列的索引
		giveUp:    false,                          // 初始状态为未放弃连接
		finish:    false,                          // 初始状态为未完成连接
		statC:     make(chan struct{}, 1),         // 创建状态通道
		tryTimes:  DefaultTryTimes,
		connected: connected,
	}
}

// AddConn 添加一个连接。
func (cm *ConnSupervisor) AddConn(p peer.AddrInfo, connected bool, mode int, nodeInfo *NodeInfo) {
	cm.cmLock.Lock()
	defer cm.cmLock.Unlock()
	// 创建
	cm.actuators[p.ID] = newTryToDialActuator(p, connected, DefaultTryTimes, mode, nodeInfo)
	logrus.Debugf("[PeerConnManager] 添加连接 (pid:%s)", p.ID.String())

}

// RemoveConn 移除一个连接。
func (cm *ConnSupervisor) RemoveConn(p peer.AddrInfo) {
	cm.cmLock.Lock()
	defer cm.cmLock.Unlock()
	// 删除节点
	delete(cm.actuators, p.ID)
}

// Connected 如果节点已连接，则返回true；否则返回false。
func (cm *ConnSupervisor) Connected(p peer.AddrInfo) bool {
	cm.cmLock.RLock()
	defer cm.cmLock.RUnlock()

	tt, ok := cm.actuators[p.ID] // 检查节点是否在节点映射中
	if ok {
		return tt.connected // 返回检查结果
	}

	return false // 返回检查结果
}

// ConnectedAddrInfo 返回连接到节点信息。
func (cm *ConnSupervisor) ConnectedAddrInfo() []peer.AddrInfo {
	cm.cmLock.RLock()
	defer cm.cmLock.RUnlock()

	var connectedPeers []peer.AddrInfo // 用于存储已连接的节点信息的切片
	for key := range cm.actuators {
		actuator, ok := cm.actuators[key] // 检查节点是否在节点映射中
		if ok {
			if actuator.connected {
				connectedPeers = append(connectedPeers, actuator.peerInfo)
			}
		}
	}
	return connectedPeers // 返回检查结果
}

// setSignal 设置 ConnSupervisor 的信号状态
func (cs *ConnSupervisor) setSignal(signal bool) {
	cs.signalLock.Lock()         // 写入锁
	defer cs.signalLock.Unlock() // 释放锁
	cs.signal = signal
}

// getSignal 获取 ConnSupervisor 的信号状态
func (cs *ConnSupervisor) getSignal() bool {
	cs.signalLock.RLock()         // 读取锁
	defer cs.signalLock.RUnlock() // 释放锁
	return cs.signal
}

// startSupervising 启动一个 goroutine 来监管连接
func (cs *ConnSupervisor) startSupervising(readySignal chan struct{}) {
	if cs.startUp {
		return
	}
	cs.setSignal(true)
	go func() {
		defer func() {
			if err := recover(); err != nil {
				logrus.Error(err) // 如果出现错误，记录错误

			}
		}()
		cs.startUp = true
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-readySignal: // 等待就绪信号
		case <-timer.C: // 或者等待10秒钟
		}
		for cs.getSignal() {
			cs.try()                    // 尝试连接
			time.Sleep(5 * time.Second) // 每5秒钟尝试一次
		}
		cs.startUp = false
	}()
}

// getPeerAddrInfos 获取用于监管的对等节点的地址信息
func (cs *ConnSupervisor) getPeerAddrInfos() []peer.AddrInfo {
	cs.peerAddrInfosLock.RLock()         // 读取锁
	defer cs.peerAddrInfosLock.RUnlock() // 释放锁
	return cs.peerAddrInfos
}

// refreshPeerAddrInfos 刷新用于监管的对等节点的地址信息
func (cs *ConnSupervisor) refreshPeerAddrInfos(peerAddrInfos []peer.AddrInfo) {
	cs.peerAddrInfosLock.Lock()         // 写入锁
	defer cs.peerAddrInfosLock.Unlock() // 释放锁
	cs.peerAddrInfos = peerAddrInfos
}

// try 方法尝试连接到所有未连接的对等节点
// 主要目的是尝试连接到所有未连接的对等节点，如果所有的对等节点都已连接，那么它将记录这个信息并设置所有节点已连接的状态为真。
// 如果某个对等节点没有连接，那么它将创建一个新的尝试拨号行为器并在新的 Goroutine 中运行它。
func (cs *ConnSupervisor) try() {
	if len(cs.peerAddrInfos) > 0 { // 如果对等节点地址信息不为空
		cs.tryConnectLock.Lock()         // 获取锁
		defer cs.tryConnectLock.Unlock() // 释放锁，确保在函数结束时释放

		for _, p := range cs.getPeerAddrInfos() { // 遍历对等节点地址信息

			// 判断是否已经连接过
			if !cs.Connected(p) {
				// AddAddrs 使用给定的 ttl（生存时间）提供此 AddrBook 地址以供使用，之后该地址不再有效。 如果管理器的 TTL 更长，则该操作对该地址是空操作
				cs.host.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)

				handshake, err := handshakeAgreement(cs.ctx, cs.host, cs.nodeInfo, p.ID, HandshakeProtocol, int(cs.peerDHT.Mode()))
				if err != nil {
					// 如果错误继续下一个同行
					// TODO:后面补充节点信息
					cs.AddConn(p, false, -1, nil) // 将新的连接添加到连接管理器中
					cs.tryConnectLock.Unlock()    // 如果对等节点已连接或者是当前节点自身，则解锁并继续下一次循环
					continue
				}

				table, exists := cs.routingTables[handshake.ModeId]
				if exists {
					// 键存在，可以使用 value
					// 更新Kbucket路由表
					renewKbucketRoutingTable(table, handshake.ModeId, p.ID)

				} else {
					// 键不存在
					table, err := NewTable(cs.host)
					if err != nil {
						logrus.Error("新建k桶失败", err)
					}
					// 更新Kbucket路由表
					renewKbucketRoutingTable(table, handshake.ModeId, p.ID)
					//
					cs.routingTables[handshake.ModeId] = table
				}

				logrus.Info("地址簿连接成功", p.ID)
				// TODO:后面补充节点信息
				cs.AddConn(p, true, handshake.ModeId, handshake.NodeInfo) // 将新的连接添加到连接管理器中

				cs.tryConnectLock.Unlock() // 解锁
				continue
			}
			cs.tryConnectLock.Unlock() // 解锁
		}
	}
}
