// kad-dht

package dep2p

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/sirupsen/logrus"
)

// startNetworkSubscriber 该方法的作用是启动网络订阅器，用于订阅事件并处理相应的回调。
func (dht *DeP2PDHT) startNetworkSubscriber() error {
	// 启动网络订阅器函数，用于订阅事件并处理相应的回调
	bufSize := eventbus.BufSize(256)

	evts := []interface{}{
		// 注册对等方何时成功完成识别的事件总线通知，以便更新路由表
		new(event.EvtPeerIdentificationCompleted),

		// 注册事件总线协议 ID 更改以更新路由表
		new(event.EvtPeerProtocolsUpdated),

		// 当我们的本地地址发生变化时注册事件总线通知，以便我们可以将这些通知发布到网络
		new(event.EvtLocalAddressesUpdated),

		// 我们想知道我们何时与其他同伴断开连接。
		new(event.EvtPeerConnectednessChanged),
	}

	// 注册事件总线本地可路由性更改，以便触发客户端和服务器模式之间的切换
	// 仅当 DHT 在 ModeAuto 下运行时才注册事件
	// 如果 DHT 正在以 ModeAuto 模式运行，则注册事件总线本地可达性更改通知，以触发客户端和服务器模式之间的切换
	if dht.auto == ModeAuto || dht.auto == ModeAutoServer {
		evts = append(evts, new(event.EvtLocalReachabilityChanged))
	}

	subs, err := dht.host.EventBus().Subscribe(evts, bufSize)
	if err != nil {
		return fmt.Errorf("dht could not subscribe to eventbus events: %w", err)
	}

	dht.wg.Add(1)
	go func() {
		defer dht.wg.Done()
		defer subs.Close()

		for {
			select {
			case e, more := <-subs.Out():
				if !more {
					return
				}

				switch evt := e.(type) {
				case event.EvtLocalAddressesUpdated:
					// 当我们的地址发生变化时，我们应该主动告知我们最亲近的同事，以便我们很快被发现。
					// 识别协议将使用我们的新地址将签名的对等记录推送到我们连接到的所有对等点。
					// 然而，我们可能不一定与我们的亲密同伴有联系，因此本着真正的禅宗精神，在网络中寻找自己确实是与这些事物建立联系的最佳方式。
					if dht.autoRefresh || dht.testAddressUpdateProcessing {
						dht.rtRefreshManager.RefreshNoWait()
					}
				case event.EvtPeerProtocolsUpdated:
					handlePeerChangeEvent(dht, evt.Peer)
				case event.EvtPeerIdentificationCompleted:
					handlePeerChangeEvent(dht, evt.Peer)
				case event.EvtPeerConnectednessChanged:
					// 如果对等方的连接状态不是 Connected，则调用 OnDisconnect 方法处理断开连接的事件
					if evt.Connectedness != network.Connected {
						dht.msgSender.OnDisconnect(dht.ctx, evt.Peer)
					}
				case event.EvtLocalReachabilityChanged:
					// 如果 DHT 正在以 ModeAuto 或 ModeAutoServer 模式运行，则处理本地可达性更改事件
					if dht.auto == ModeAuto || dht.auto == ModeAutoServer {
						handleLocalReachabilityChangedEvent(dht, evt)
					} else {
						// 如果我们收到一个我们没有订阅的事件，那就真的出了问题
						logrus.Errorf("received LocalReachabilityChanged event that was not subscribed to")
					}
				default:
					// 如果我们收到另一种类型的事件，那就真的出了问题
					logrus.Errorf("got wrong type from subscription: %T", e)
				}
			case <-dht.ctx.Done():
				return
			}
		}
	}()

	return nil
}

// validRTPeer 如果对等方支持 DHT 协议，则返回 true，否则返回 false。
// 支持 DHT 协议意味着支持主要协议，我们不想将正在使用过时的辅助协议的对等点添加到我们的路由表中
func (dht *DeP2PDHT) validRTPeer(p peer.ID) (bool, error) {
	// 获取对等节点支持的首个匹配协议
	b, err := dht.peerstore.FirstSupportedProtocol(p, dht.protocols...)
	if len(b) == 0 || err != nil {
		// 如果协议列表为空或出现错误，则返回 false 和错误信息
		return false, err
	}

	// 如果路由表的对等节点过滤器为空，或者对等节点通过过滤器的检查，则返回 true
	return dht.routingTablePeerFilter == nil || dht.routingTablePeerFilter(dht, p), nil
}

// handlePeerChangeEvent 该函数的作用是处理对等方更改事件，并根据对等方的有效性调用不同的方法进行处理。
func handlePeerChangeEvent(dht *DeP2PDHT, p peer.ID) {
	// 处理对等方更改事件的回调函数，参数为 DHT 对象和对等方 ID
	valid, err := dht.validRTPeer(p)
	if err != nil {
		// 如果无法检查 peerstore 的协议支持情况，则记录错误日志并返回
		logrus.Errorf("could not check peerstore for protocol support: err: %s", err)
		return
	} else if valid {
		// 如果对等方有效，则调用 peerFound 方法处理对等方的发现
		dht.peerFound(p)
	} else {
		// 如果对等方无效，则调用 peerStoppedDHT 方法停止 DHT
		dht.peerStoppedDHT(p)
	}
}

// handleLocalReachabilityChangedEvent 该方法的作用是处理本地可达性更改事件的回调函数。
func handleLocalReachabilityChangedEvent(dht *DeP2PDHT, e event.EvtLocalReachabilityChanged) {
	// 处理本地可达性更改事件的回调函数，根据可达性设置 DHT 的模式
	var target mode

	switch e.Reachability {
	case network.ReachabilityPrivate:
		// 如果可达性为私有网络，将目标模式设置为客户端模式
		target = modeClient
	case network.ReachabilityUnknown:
		// 如果可达性未知，根据 DHT 的自动模式设置确定目标模式
		if dht.auto == ModeAutoServer {
			// 如果 DHT 的自动模式为 ModeAutoServer，则将目标模式设置为服务器模式
			target = modeServer
		} else {
			// 否则，将目标模式设置为客户端模式
			target = modeClient
		}
	case network.ReachabilityPublic:
		// 如果可达性为公共网络，将目标模式设置为服务器模式
		target = modeServer
	}

	logrus.Infof("processed event %T; performing dht mode switch", e)

	err := dht.setMode(target)
	// 注意：模式将以十进制打印出来
	if err == nil {
		logrus.Infoln("switched DHT mode successfully", "mode", target)
	} else {
		logrus.Errorln("switching DHT mode failed", "mode", target, "error", err)
	}
}
