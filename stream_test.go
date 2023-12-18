package dep2p

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bpfs/dep2p/streams"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/sirupsen/logrus"

	"github.com/libp2p/go-libp2p/core/host"
)

const (
	TestProtocol = "/bpfs/testProtocol/1.0.0"
)

// 协议
type Protocol struct {
	Ctx  context.Context
	Host host.Host // 网络主机

}

// go test -v -bench=. -benchtime=10m ./... -run TestStream
func TestStream(t *testing.T) {

	// 创建DeP2P node01
	node01, err := makeDeP2P()
	if err != nil {
		panic(err)
	}
	logrus.Printf("node01[%s] 启动\n", node01.Host().ID())

	// 创建DeP2P node02
	node02, err := makeDeP2P()
	if err != nil {
		panic(err)
	}

	logrus.Printf("node02[%s] 启动\n", node02.Host().ID())

	// node01 注册事件
	node01p := &Protocol{
		Ctx:  node01.Context(),
		Host: node01.Host(),
	}

	streams.RegisterStreamHandler(node01.Host(), TestProtocol, streams.HandlerWithRead(node01p.handleRequestStream))

	// node02 注册事件
	node02p := &Protocol{
		Ctx:  node02.Context(),
		Host: node02.Host(),
	}
	streams.RegisterStreamHandler(node02.Host(), TestProtocol, streams.HandlerWithRead(node02p.handleRequestStream))

	// 连接两个主机
	if err := connectNode(t, node01.Host(), node02.Host()); err != nil {
		t.Fatal(err)
	}
	// 等待一段时间以确保生效
	time.Sleep(time.Millisecond * 100)

	// 如果不使用connectNode 等待节点发现，使用以下方法
	//time.Sleep(time.Minute * 1)

	// node01 发送node02
	handleStream(t, node01.Context(), node01.Host(), node02.Host().ID(), TestProtocol)
	// 等待一段时间以确保收到消息
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

// 流处理协议
func (p *Protocol) handleRequestStream(request *streams.RequestMessage) {
	fmt.Println("===测试流发送方：", request.Message.Sender)
	fmt.Println("===测试流消息：", string(request.Payload))
}

// 发送内容
func handleStream(t *testing.T, ctx context.Context, h host.Host, pid peer.ID, ptl string) {
	// 准备请求消息
	payload := []byte("message")
	request := &streams.RequestMessage{
		Message: &streams.Message{
			Sender:   h.ID().String(), // 发送方ID
			Receiver: pid.String(),    // 接收方ID
		},
		Payload: payload,
	}

	// 序列化请求
	reqByte, err := request.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	// 建立流连接
	stream, err := h.NewStream(ctx, pid, protocol.ID(ptl))
	if err != nil {
		t.Fatal(err)
	}

	// 关闭流
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(time.Second * 8))

	// 将消息写入流
	if err = streams.WriteStream(reqByte, stream); err != nil {
		t.Fatal(err)
	}

}
