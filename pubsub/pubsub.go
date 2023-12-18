// backend/libp2p/pubsub.go

package pubsub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bpfs/dep2p/streams"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
)

const (

	// DefaultLibp2pPubSubMaxMessageSize 是 pub-sub 的默认最大消息大小。
	DefaultLibp2pPubSubMaxMessageSize = 50 << 20

	// DefaultPubsubProtocol 默认 dep2p 订阅协议
	DefaultPubsubProtocol = "/dep2p/pubsub/1.0.0"
)

// PubSubMsgHandler handle the msg published by other node.
type PubSubMsgHandler func(*streams.RequestMessage)

// DeP2PPubSub 是一个 pub-sub 服务实现。
type DeP2PPubSub struct {
	ctx              context.Context // 上下文
	host             host.Host       // 主机
	topicLock        sync.Mutex
	topicMap         map[string]*pubsub.Topic        // topicMap 将 topic 名称映射到 Topic。
	pubsub           *pubsub.PubSub                  // pubsub 是一个 pubsub.PubSub 实例。
	startUp          int32                           // startUp 是状态的标志。0 未启动，1 正在启动，2 已启动。
	subscribedTopics map[string]*pubsub.Subscription // map[chainId]*topicSubscription (topicSubscription 是一个结构体，它包含了一些关于主题订阅的信息)
	subscribeLock    sync.Mutex                      // 互斥锁，用于订阅时的线程安全

}

// NewPubsub 创建一个新的 LibP2pPubSub 实例。
func NewPubsub(ctx context.Context, host host.Host) (*DeP2PPubSub, error) {

	// 创建一个新的 LibP2pPubSub 实例
	ps := &DeP2PPubSub{
		ctx:              ctx,
		host:             host,
		topicMap:         make(map[string]*pubsub.Topic),        // 创建一个新的主题映射                                 // 设置最大消息大小
		startUp:          0,                                     // 设置启动状态为 0
		subscribedTopics: make(map[string]*pubsub.Subscription), // 创建一个新的订阅主题 map
	}

	return ps, nil
}

// GetTopic 根据给定的名称获取一个 topic。
func (ps *DeP2PPubSub) GetTopic(name string) (*pubsub.Topic, error) {
	// 如果 pub-sub 服务还没有完全启动，返回错误
	if atomic.LoadInt32(&ps.startUp) < 2 {
		return nil, fmt.Errorf("libp2p gossip-sub 未运行")
	}
	// 获取主题映射的读取锁
	ps.topicLock.Lock()
	defer ps.topicLock.Unlock()
	// 如果主题映射中存在该主题，返回该主题
	t, ok := ps.topicMap[name]
	if !ok || t == nil {
		// 否则，加入该主题，并将其添加到主题映射中
		topic, err := ps.pubsub.Join(name)
		if err != nil {
			return nil, err
		}

		ps.topicMap[name] = topic
		t = topic
	}
	// 返回主题
	return t, nil
}

// Subscribe 订阅一个 topic。
func (bps *DeP2PPubSub) Subscribe(topic string, subscribe bool) (*pubsub.Subscription, error) {
	if topic == "" {
		topic = DefaultPubsubProtocol
	}
	// 获取主题
	t, err := bps.GetTopic(topic)
	if err != nil {
		return nil, err
	}
	// 订阅主题，并返回订阅
	logrus.Infof("[PubSub] gossip-sub 订阅 topic[%s]。", topic)

	if subscribe {
		return t.Subscribe()
	}

	return nil, nil
}

// Publish 向 topic 发布一条消息。
func (bps *DeP2PPubSub) Publish(topic string, data []byte) error {
	// 获取主题
	t, err := bps.GetTopic(topic)
	if err != nil {
		return err
	}
	// 向主题发布消息，并返回发布结果
	return t.Publish(bps.ctx, data)
}

// Start 方法用于启动 pub-sub 服务。
// 它首先检查主机是否正在运行，然后检查 pub-sub 服务是否已经启动。如果 pub-sub 服务尚未启动，它会设置启动状态为 1 并启动 pub-sub 服务。如果在启动过程中出现错误，它会返回错误。否则，它会将新创建的 pub-sub 服务赋值给 ps.pubsub，并将启动状态设置为 2。
func (bps *DeP2PPubSub) Start(opts ...pubsub.Option) error {

	// 检查 pub-sub 服务是否已经启动，如果已经启动，则返回 nil
	if atomic.LoadInt32(&bps.startUp) > 0 {
		logrus.Warnf("[PubSub] gossip-sub 服务 正在运行。")
		return nil
	}
	// 如果 pub-sub 服务尚未启动，则设置启动状态为 1 并启动 pub-sub 服务
	atomic.StoreInt32(&bps.startUp, 1)
	logrus.Infof("[PubSub] gossip-sub 服务 正在启动... ")

	pss, err := pubsub.NewGossipSub(
		bps.ctx,
		bps.host,
		opts...,
	)
	// 如果在启动过程中出现错误，则返回错误
	if err != nil {
		return err
	}
	// 否则，将新创建的 pub-sub 服务赋值给 ps.pubsub，并将启动状态设置为 2
	bps.pubsub = pss
	atomic.StoreInt32(&bps.startUp, 2)
	logrus.Infof("[PubSub] gossip-sub 服务 已启动。")
	return nil
}

// isSubscribed 方法检查给定的链ID和主题是否已经订阅
func (bps *DeP2PPubSub) IsSubscribed(topic string) bool {
	_, ok := bps.subscribedTopics[topic] // 检查主题是否已经订阅
	return ok                            // 返回检查结果
}

// BroadcastWithTopic 方法将消息广播到给定链ID的目标链中的给定主题
func (bps *DeP2PPubSub) BroadcastWithTopic(topic string, data []byte) error {
	bytes := data

	_, ok := bps.subscribedTopics[topic] // 获取给定链ID的 LibP2pPubSub 实例
	if !ok {
		return fmt.Errorf("主题未订阅") // 如果不存在，则返回错误
	}

	return bps.Publish(topic, bytes) // 发布消息
}

// CancelSubscribeWithTopic 取消订阅给定主题
func (bps *DeP2PPubSub) CancelSubscribeWithTopic(topic string) error {
	// 如果 pub-sub 服务还没有完全启动，返回错误
	if atomic.LoadInt32(&bps.startUp) < 2 {
		return fmt.Errorf("libp2p gossip-sub 未运行")
	}
	// 获取主题映射的读取锁
	bps.subscribeLock.Lock()
	defer bps.subscribeLock.Unlock()

	if topicSub, ok := bps.subscribedTopics[topic]; ok {
		if topicSub != nil {
			topicSub.Cancel()
		}
		delete(bps.subscribedTopics, topic)
	}
	return nil
}

// CancelPubsubWithTopic 取消给定名字的订阅
func (bps *DeP2PPubSub) CancelPubsubWithTopic(name string) error {
	// 如果 pub-sub 服务还没有完全启动，返回错误
	if atomic.LoadInt32(&bps.startUp) < 2 {
		return fmt.Errorf("libp2p gossip-sub 未运行")
	}
	// 获取主题映射的读取锁
	bps.topicLock.Lock()
	defer bps.topicLock.Unlock()

	if topic, ok := bps.topicMap[name]; ok {
		// 关闭topic
		if err := topic.Close(); err != nil {
			return err
		}
		delete(bps.topicMap, name)
	}
	return nil
}

// SubscribeWithTopic 订阅给定链ID的目标链中的给定主题，并使用给定的订阅消息处理函数。
func (bps *DeP2PPubSub) SubscribeWithTopic(topic string, handler PubSubMsgHandler, subscribe bool) error {
	bps.subscribeLock.Lock()
	defer bps.subscribeLock.Unlock()

	// 检查是否已经订阅
	if bps.IsSubscribed(topic) {
		return fmt.Errorf("主题已订阅")
	}

	// var topicSub *pubsub.Subscription
	// var err error
	//确定订阅的节点，才会得到订阅-发布系统中的消息
	// if subscribe {
	// 	topicSub, err = bps.Subscribe(topic) // 订阅主题//订阅pubsub的pub消息
	// 	if err != nil {
	// 		return err
	// 	}
	// } else {
	// 	topicSub = nil
	// }

	topicSub, err := bps.Subscribe(topic, subscribe) // 订阅主题
	if err != nil {
		return err
	}

	bps.subscribedTopics[topic] = topicSub

	// 添加订阅信息
	if topicSub != nil {
		// 启动一个新的 goroutine 来处理订阅主题的消息
		go func() {
			// defer func() {
			// 	if err := recover(); err != nil {
			// 		if !bps.isSubscribed(topic) {
			// 			return
			// 		}
			// 		logrus.Errorf("[Net] 订阅 goroutine 恢复错误：%s", err)
			// 	}
			// }()

			bps.topicSubLoop(topicSub, topic, handler)
		}()
	}

	return nil
}

// topicSubLoop 用于处理订阅主题的消息循环
func (bps *DeP2PPubSub) topicSubLoop(
	topicSub *pubsub.Subscription,
	topic string,
	handler PubSubMsgHandler,
) {
	for {
		// 获取下一个订阅消息
		message, err := topicSub.Next(bps.ctx)

		if err != nil {
			// 如果订阅被取消，记录警告并退出循环
			if err.Error() == "subscription cancelled" {
				logrus.Warn("[Net] 订阅已取消：", err)
				break
			}
			// 如果获取下一个订阅消息失败，记录错误
			logrus.Errorf("[Net] 订阅下一个消息失败：%s", err.Error())
		}

		// 如果消息为空，返回
		if message == nil {
			return
		}

		// 过滤掉自己发布的消息
		if message.GetFrom() == bps.host.ID() {
			continue
		}
		var request streams.RequestMessage
		if err := request.Unmarshal(message.Data); err != nil {
			return
		}
		// 解码响应消息
		// request, err := utils.DeserializeRequest(message.Data)
		// if err != nil {
		// 	continue
		// }
		// 如果消息中的 Receiver 存在则为定向消息，该消息只能由 Receiver 节点处理
		// 如果 Receiver 与当前 host 的 peerId 并不一致，说明该 host 无需处理
		if request.Message.Receiver != "" && request.Message.Receiver != bps.host.ID().String() {
			continue
		}
		// 处理接收到的消息
		handler(&request)
	}
}

// Pubsub 返回 pubsub.PubSub 实例
func (bps *DeP2PPubSub) Pubsub() *pubsub.PubSub {
	return bps.pubsub
}

// ListPeers 返回我们在给定主题中连接到的对等点列表。
func (bps *DeP2PPubSub) ListPeers(topic string) []peer.ID {
	return bps.pubsub.ListPeers(topic)
}
