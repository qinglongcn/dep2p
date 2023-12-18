# DeP2P

[![Go Reference](https://pkg.go.dev/badge/github.com/klauspost/reedsolomon.svg)](https://pkg.go.dev/github.com/libp2p/libp2p) [![Go](https://github.com/klauspost/reedsolomon/actions/workflows/go.yml/badge.svg)]()

DeP2P(Decentralized peer-to-peer) 去中心化对等网络，是用 Go（golang）编写的一种 [P2P](https://baike.baidu.com/item/%E5%AF%B9%E7%AD%89%E7%BD%91%E7%BB%9C/5482934) 对等网络的便捷实现。

该项目目前正在积极开发中，处于 Beta 状态。

这是 [libp2p](https://github.com/libp2p/libp2p) 库的 Go 升级版。libp2p 是一个模块化网络框架，由 [BPFS](https://bpfs.xyz/#/) 进行了一些额外的优化，意在提升 P2P 的便捷性。

libp2p 是互联网过去所有点对点协议的集大成之作。由于 libp2p 源于 [IPFS](https://github.com/ipfs) 。所以，在其协议内部或多或少的与 IPFS 进行了一些强捆绑。另外，libp2p 作为一个"网络堆栈"，在网络协议中不可避免的使用了"内容存储"，这成为了非"存储"项目的累赘。

DeP2P 干净的分离了 IPFS 代码，并对"内容存储"等内容进行删减。同时，将 [Kademlia DHT](https://github.com/libp2p/go-libp2p-kad-dht)、[PubSub](https://github.com/libp2p/go-libp2p-pubsub) 集成后统一配置和调用。有助于 Web3 项目的快速实施和可靠运行。

对于大多数用户来说，并不关心 [Kademlia](https://baike.baidu.com/item/Kademlia/3106849?fr=ge_ala)、Discovery、PubSub 等专有技术，甚至用户完全不需要理会这个分布式网络的具体过程。用户所需要的：

1. 加入网络成为其中的一个节点；
2. 从网络中发现更多的节点；
3. 连接节点；
4. 使用 "流" 或 "订阅" 模式发送/接收消息。

基于用户上述真实需求，DeP2P 依托成熟的网络模块，构建"友好"、"健壮"、"专注"的去中心化网络基础设施，将用户从繁杂的网络开发中解放出来，以便其更好的专注于业务应用。

了解有关 DeP2P 的更多信息，请访问 [bpfs.xyz](https://bpfs.xyz/#/) 。

## 要求

[Go](http://golang.org) 1.20 或最新版本.

## 安装

要获取包，请使用标准：

```bash

    go get -u github.com/bpfs/dep2p
```

推荐使用 Go 模块。

## 用法

本节假设您具有 Go（golang）编码的基础知识。 

该包执行 P2P 去中心化网络的配置和通信。因此使用起来也比较简单。

### 前置

首先，您需要使用 NewDeP2P 创建新的 DeP2P 实例。虽然您可以使用默认的配置运行网络，但我们依然强烈建议您对相关参数进行配置。当然，这很大程度上取决于您的使用场景。不过您放心，文后我们会附上可直接运行的配置供您修改。

```Go

    func dep2p.NewDeP2P(ctx context.Context, opts ...dep2p.OptionDeP2P) (*dep2p.DeP2P, error)
```

然后，如果您需要使用"订阅"模式传递消息，还需要使用 NewDeP2PPubSub 创建新的 DeP2P 主题。

```Go

    func NewDeP2PPubSub(ctx context.Context, p2p *dep2p.DeP2P) (*pubsub.DeP2PPubSub, error)
```

现在，您的网络应该已经正常运行了。接下来，我们开始准备发送和接收信息。

### 注册

首先，我们需要进行"注册"，以便让节点知道您将使用那个方法来接收消息。

a. 下面是对"流"的注册，通俗的讲就是将消息从 节点① 发送给 节点② ，并可接收 节点② 回复的信息。

```Go

    func streams.RegisterStreamHandler(h host.Host, p protocol.ID, handler network.StreamHandler)
```

b. 下面是对"订阅"的注册，通俗的讲就是 "群" 消息，由有限个节点组建一个群(或通道)，您可以将消息发给群所有节点或其中的某个节点。当然，您也可以设置让部分群节点，只能发送消息，不允许其接收（这在某些应用场景非常有意义）。

```Go

    func (*pubsub.DeP2PPubSub).SubscribeWithTopic(topic string, handler pubsub.PubSubMsgHandler, subscribe bool) error
```

系统设计时，您可以根据不同的通信需求，针对 "流" 和 "订阅" 设置不同的 "协议"。您可以将每个 "协议" 想象为城市（节点）间的网络：高速、国道、地铁、航线……

接下来，您只需要专注于 【发送】 和 【接收】 信息。

### 通信

a. 使用 NewStream 打开一个新流到给定的对等点 p，并使用给定的 ProtocolID 写入 p2p 协议标头。 如果没有到 p 的连接，则尝试创建一个。
这个协议需要是您在此前已经注册了的。

```Go

    func (host.Host).NewStream(ctx context.Context, p peer.ID, pids ...protocols.ID) (network.Stream, error)
```

接收该消息的是您在前面注册时的 'handler network.StreamHandler'，下面是一个应用实例：

```Go
    import (
        "github.com/bpfs/dep2p"
        "github.com/bpfs/dep2p/streams"
    )

    // 流协议
    const (
        // 协议
        StreamExampleProtocol = "dep2p@stream:example/1.0.0"
    )

    type Input struct {
        fx.In
        Ctx          context.Context   
        P2P          *dep2p.DeP2P   
    }

    // RegisterStreamProtocol 注册流
    func RegisterStreamProtocol(lc fx.Lifecycle, input Input) {
        // 流协议
        sp := &StreamProtocol{
            ctx:          input.Ctx,
            p2p:          input.P2P,
        }

        // 注册流
        streams.RegisterStreamHandler(input.P2P.Host(), StreamExampleProtocol, streams.HandlerWithRW(sp.HandleStreamExampleStream))

        // 您可以按需继续注册其他流协议

        lc.Append(fx.Hook{
            OnStop: func(ctx context.Context) error {
                return nil
            },
        })
    }

    ////////////////////////////////////////////////////////////////

    // 流协议
    type StreamProtocol struct {
        ctx          context.Context         
        p2p          *dep2p.DeP2P        
    }

    // HandleStreamExampleStream 处理流消息
    func (sp *StreamProtocol) HandleStreamExampleStream(req *streams.RequestMessage, res *streams.ResponseMessage) error {
        // 省略处理逻辑
    }
```

b. 使用 BroadcastWithTopic 将消息广播到给定的主题。其中，可以指定主题中特别的某个节点。这个主题需要是您在此前已经注册了的。

```Go

    func (*pubsub.DeP2PPubSub).BroadcastWithTopic(topic string, data []byte) error
```

下面是构建 data 消息体的内容。其中，Payload 是您要传输的内容；而 Message 中的 Type 是您可自定义的类（便于您接收消息后处理）；Sender 是发送方（通常为您自己）；Receiver 是接收方（不设置的话，该通道内所有节点都可以接收到此消息）。

```Go
    type RequestMessage struct {
        Message              *Message `protobuf:"bytes,1,opt,name=message,proto3" json:"message,omitempty"`
        Payload              []byte   `protobuf:"bytes,2,opt,name=payload,proto3" json:"payload,omitempty"`
        XXX_NoUnkeyedLiteral struct{} `json:"-"`
        XXX_unrecognized     []byte   `json:"-"`
        XXX_sizecache        int32    `json:"-"`
    }

    type Message struct {
        Type                 string   `protobuf:"bytes,1,opt,name=type,proto3" json:"type,omitempty"`
        Sender               string   `protobuf:"bytes,2,opt,name=sender,proto3" json:"sender,omitempty"`
        Receiver             string   `protobuf:"bytes,3,opt,name=receiver,proto3" json:"receiver,omitempty"`
        XXX_NoUnkeyedLiteral struct{} `json:"-"`
        XXX_unrecognized     []byte   `json:"-"`
        XXX_sizecache        int32    `json:"-"`
    }
```

接收该消息的是您在前面注册时的 'handler pubsub.PubSubMsgHandler'，下面是一个应用实例：

```Go
    import (
        "github.com/bpfs/dep2p"
        "github.com/bpfs/dep2p/streams"
        "github.com/bpfs/dep2p/pubsub"
    )

    // 订阅主题
    const (
        PubsubExampleTopic = "dep2pe@pubsub:example/1.0.0"
    )

    type Input struct {
        fx.In
        Ctx          context.Context    
        P2P          *dep2p.DeP2P   
        PubSub       *pubsub.DeP2PPubSub 
    }

    // RegisterPubsubProtocol 注册订阅
    func RegisterPubsubProtocol(lc fx.Lifecycle, input Input) {
        // 主题
        if err := input.PubSub.SubscribeWithTopic(PubsubExampleTopic, func(res *streams.RequestMessage) {
            HandleExamplePubSub(input.P2P, input.PubSub, res)
        }, true); err != nil {
            logrus.Errorf("注册失败：%v \n", err)
        }

        // 您可以按需继续注册其他订阅协议
    
        lc.Append(fx.Hook{
            OnStop: func(ctx context.Context) error {
                return nil
            },
        })
    }

    ////////////////////////////////////////////////////////////////

    // HandleExamplePubSub 处理订阅消息
    func HandleExamplePubSub(p2p *dep2p.DeP2P, pubsub *pubsub.DeP2PPubSub, res *streams.RequestMessage) {
        // 省略处理逻辑
    }

```

### 文档

该文档正在编写中……
