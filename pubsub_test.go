package dep2p

import (
	"fmt"
	"testing"
	"time"

	"github.com/bpfs/dep2p/pubsub"
	"github.com/bpfs/dep2p/streams"

	libp2ppubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p-pubsub/timecache"
	"github.com/sirupsen/logrus"
)

// go test -v -bench=. -benchtime=10m ./... -run TestPubsub
func TestPubsub(t *testing.T) {

	// 创建DeP2P node01
	node01, err := makeDeP2P()
	if err != nil {
		panic(err)
	}
	logrus.Printf("node01[%s] 启动\n", node01.Host().ID())

	// 初始化 PubSub node01
	ps1, err := pubsub.NewPubsub(node01.Context(), node01.Host()) // 初始化 PubSub
	if err != nil {
		t.Fatal("node03 初始化 PubSub 失败,", err)
	}

	// 返回新创建的 LibP2pPubSub 实例
	if err := ps1.Start(buildPubSub()...); err != nil {
		t.Fatal("node01 初始化 PubSub 失败,", err)
	}

	// 订阅主题
	topicName := "TEST_TOPIC"

	//  订阅信息
	err = ps1.SubscribeWithTopic(topicName, func(msgData *streams.RequestMessage) {
		logrus.Printf("========================>node01 收到订阅信息[%s],消息:[%v]", topicName, string(msgData.Message.Type))

	}, true)

	if err != nil {
		t.Fatal("node01 初始化 SubscribeWithTopic 失败,", err)
	}

	// 创建DeP2P node02
	node02, err := makeDeP2P()
	if err != nil {
		panic(err)
	}
	logrus.Printf("node02[%s] 启动\n", node02.Host().ID())

	// 初始化 PubSub node02
	ps2, err := pubsub.NewPubsub(node02.Context(), node02.Host()) // 初始化 PubSub
	if err != nil {
		t.Fatal("node02 初始化 PubSub 失败:", err)
	}
	// 启动服务
	if err := ps2.Start(buildPubSub()...); err != nil {
		t.Fatal("node01 初始化 PubSub 失败,", err)
	}

	//  订阅信息
	err = ps2.SubscribeWithTopic(topicName, func(msgData *streams.RequestMessage) {
		logrus.Printf("========================>node02 收到订阅信息[%s],消息:[%v]", topicName, string(msgData.Payload))

	}, true)
	if err != nil {
		t.Fatal("node02 初始化 SubscribeWithTopic 失败,", err)
	}

	// 连接两个主机
	if err := connectNode(t, node01.Host(), node02.Host()); err != nil {
		panic(err)
	}

	// 等待一段时间以确保订阅生效
	time.Sleep(time.Millisecond * 100)
	// 如果不使用connectNode 等待节点发现，使用以下方法
	// time.Sleep(time.Minute * 1)
	// 设置参数
	request := &streams.RequestMessage{
		Message: &streams.Message{
			Type:   "requestTest",               // 消息类型
			Sender: node01.Host().ID().String(), // 发送方ID
		},
		Payload: []byte("我是 node01"),
	}
	// request.Message.Type = "requestTest"                 // 消息类型
	// request.Message.Sender = node01.Host().ID().String() // 发送方ID
	//request.Receiver = node02.Host().ID() // 接收方ID

	requestBytes, err := request.Marshal()
	if err != nil {
		t.Fatal("node03 初始化 PubSub 失败,", err)
	}

	// node01 广播信息
	err = ps1.BroadcastWithTopic(topicName, requestBytes) // 广播信息
	if err != nil {
		t.Fatal("node01 广播信息失败，", err)
	}

	fmt.Println("node01 广播信息成功")

	// 等待一段时间以确保收到
	time.Sleep(time.Second * 5)
	// 停止node01
	err = node01.Stop()
	if err != nil {
		t.Fatal("node01 停止出错", err)
	}

	// 停止node02
	err = node02.Stop()
	if err != nil {
		t.Fatal("node02 停止出错", err)
	}
}

func buildPubSub() []libp2ppubsub.Option {
	var pubsubOptions []libp2ppubsub.Option

	ttl, err := time.ParseDuration("10s")
	if err != nil {
		panic(err)
	}

	pubsubOptions = append(
		pubsubOptions,
		// 设置 pubsub 有线消息的全局最大消息大小。 默认值为 1MiB (DefaultMaxMessageSize)
		libp2ppubsub.WithMaxMessageSize(pubsub.DefaultLibp2pPubSubMaxMessageSize),
		// WithSeenMessagesTTL 配置何时可以忘记以前看到的消息 ID
		libp2ppubsub.WithSeenMessagesTTL(ttl),
		// Stategy_LastSeen 使上次被 Add 或 Has 触及的条目过期。
		libp2ppubsub.WithSeenMessagesStrategy(timecache.Strategy_LastSeen),
	)
	return pubsubOptions
}
