package net

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/sirupsen/logrus"

	"github.com/libp2p/go-msgio"

	//lint:ignore SA1019 TODO migrate away from gogo pb
	"github.com/libp2p/go-msgio/protoio"

	"go.opencensus.io/stats"
	"go.opencensus.io/tag"

	"github.com/bpfs/dep2p/internal"
	"github.com/bpfs/dep2p/metrics"
	pb "github.com/bpfs/dep2p/pb"
)

var dhtReadMessageTimeout = 10 * time.Second

// ErrReadTimeout is an error that occurs when no message is read within the timeout period.
var ErrReadTimeout = fmt.Errorf("timed out reading response")

// messageSenderImpl 负责有效地向对等点发送请求和消息，包括流的重用。
// 它还跟踪发送的请求和消息的指标。
type messageSenderImpl struct {
	host      host.Host                      // 我们需要的网络服务
	smlk      sync.Mutex                     // 用于保护 strmap 的互斥锁
	strmap    map[peer.ID]*peerMessageSender // 用于跟踪已发送请求和消息的映射
	protocols []protocol.ID                  // 支持的协议列表
}

// NewMessageSenderImpl 创建一个 messageSenderImpl 实例。
// 它接受一个 Host 实例和协议 ID 的列表作为参数，并返回一个实现了 MessageSenderWithDisconnect 接口的实例。
func NewMessageSenderImpl(h host.Host, protos []protocol.ID) pb.MessageSenderWithDisconnect {
	return &messageSenderImpl{
		host:      h,
		strmap:    make(map[peer.ID]*peerMessageSender),
		protocols: protos,
	}
}

// OnDisconnect 是 messageSenderImpl 结构体的方法。
// 当与对等点的连接断开时，该方法会被调用。
// 它接受一个上下文对象和对等点的 peer.ID 作为参数。
func (m *messageSenderImpl) OnDisconnect(ctx context.Context, p peer.ID) {
	m.smlk.Lock()
	defer m.smlk.Unlock()
	ms, ok := m.strmap[p]
	if !ok {
		return
	}
	// 从 strmap 中删除对应的对等点
	delete(m.strmap, p)

	// 异步执行此操作，因为 ms.lk 可能会阻塞一段时间。
	go func() {
		if err := ms.lk.Lock(ctx); err != nil {
			return
		}
		defer ms.lk.Unlock()
		// 使 ms 无效
		ms.invalidate()
	}()
}

// SendRequest 发送请求，但也确保测量 RTT 以进行延迟测量。
func (m *messageSenderImpl) SendRequest(ctx context.Context, p peer.ID, pmes *pb.Message) (*pb.Message, error) {
	ctx, _ = tag.New(ctx, metrics.UpsertMessageType(pmes))

	// 获取与对等点 p 相关的消息发送者
	ms, err := m.messageSenderForPeer(ctx, p)
	if err != nil {
		// 记录发送请求和发送请求错误的指标
		stats.Record(ctx,
			metrics.SentRequests.M(1),
			metrics.SentRequestErrors.M(1),
		)
		// 打印请求失败的日志
		logrus.Debug("请求发送失败，无法打开消息发送者", "错误", err, "目标", p)
		return nil, err
	}

	start := time.Now() // 记录请求开始时间

	// 发送请求并获取响应
	rpmes, err := ms.SendRequest(ctx, pmes)
	if err != nil {
		// 记录发送请求和发送请求错误的指标
		stats.Record(ctx,
			metrics.SentRequests.M(1),
			metrics.SentRequestErrors.M(1),
		)
		// 打印请求失败的日志
		logrus.Debug("请求发送失败", "错误", err, "目标", p)
		return nil, err
	}

	// 记录发送请求、发送字节数和出站请求延迟的指标
	stats.Record(ctx,
		metrics.SentRequests.M(1),
		metrics.SentBytes.M(int64(pmes.Size())),
		metrics.OutboundRequestLatency.M(float64(time.Since(start))/float64(time.Millisecond)),
	)
	m.host.Peerstore().RecordLatency(p, time.Since(start)) // 记录对等点的延迟
	return rpmes, nil
}

// SendMessage 发出一条消息
func (m *messageSenderImpl) SendMessage(ctx context.Context, p peer.ID, pmes *pb.Message) error {
	ctx, _ = tag.New(ctx, metrics.UpsertMessageType(pmes))

	// 获取与对等点 p 相关的消息发送者
	ms, err := m.messageSenderForPeer(ctx, p)
	if err != nil {
		// 记录发送消息和发送消息错误的指标
		stats.Record(ctx,
			metrics.SentMessages.M(1),
			metrics.SentMessageErrors.M(1),
		)
		// 打印消息发送失败的日志
		logrus.Debug("消息发送失败，无法打开消息发送者", "错误", err, "目标", p)
		return err
	}

	// 使用消息发送者发送消息
	if err := ms.SendMessage(ctx, pmes); err != nil {
		// 记录发送消息和发送消息错误的指标
		stats.Record(ctx,
			metrics.SentMessages.M(1),
			metrics.SentMessageErrors.M(1),
		)
		// 打印消息发送失败的日志
		logrus.Debug("消息发送失败", "错误", err, "目标", p)
		return err
	}

	// 记录发送消息和发送字节数的指标
	stats.Record(ctx,
		metrics.SentMessages.M(1),
		metrics.SentBytes.M(int64(pmes.Size())),
	)
	return nil
}

// messageSenderForPeer 用于获取与对等点相关的消息发送者
func (m *messageSenderImpl) messageSenderForPeer(ctx context.Context, p peer.ID) (*peerMessageSender, error) {
	// 加锁以保证并发安全
	m.smlk.Lock()
	ms, ok := m.strmap[p]
	if ok {
		// 如果已存在与对等点 p 相关的消息发送者，则直接返回
		m.smlk.Unlock()
		return ms, nil
	}

	// 如果不存在与对等点 p 相关的消息发送者，则创建一个新的消息发送者
	ms = &peerMessageSender{p: p, m: m, lk: internal.NewCtxMutex()}
	m.strmap[p] = ms
	m.smlk.Unlock()

	// 准备或使消息发送者无效
	if err := ms.prepOrInvalidate(ctx); err != nil {
		// 加锁以保证并发安全
		m.smlk.Lock()
		defer m.smlk.Unlock()

		if msCur, ok := m.strmap[p]; ok {
			// 发生了变化。使用新的消息发送者，旧的无效并且不在映射中，因此可以直接丢弃。
			if ms != msCur {
				return msCur, nil
			}
			// 没有变化，从映射中删除现在无效的流。
			delete(m.strmap, p)
		}
		// 无效但不在映射中。可能已被断开连接移除。
		return nil, err
	}

	// 准备就绪，可以使用了
	return ms, nil
}

// peerMessageSender 负责向特定对等方发送请求和消息
type peerMessageSender struct {
	s  network.Stream     // 用于发送请求和消息的网络流
	r  msgio.ReadCloser   // 用于读取响应的读取器和关闭器
	lk internal.CtxMutex  // 上下文锁，用于保护并发访问
	p  peer.ID            // 对等点的标识符
	m  *messageSenderImpl // 消息发送者实现的引用

	invalid   bool // 标记消息发送者是否无效
	singleMes int  // 记录已发送的单个消息数
}

// invalidate 在从 strmap 中删除此 peerMessageSender 之前调用。
// 它可以防止peerMessageSender被重用/重新初始化然后被遗忘（保持流打开）。
func (ms *peerMessageSender) invalidate() {
	ms.invalid = true
	if ms.s != nil {
		_ = ms.s.Reset()
		ms.s = nil
	}
}

// prepOrInvalidate 在准备或使无效之前对 peerMessageSender 执行上下文加锁操作。
func (ms *peerMessageSender) prepOrInvalidate(ctx context.Context) error {
	if err := ms.lk.Lock(ctx); err != nil {
		return err
	}
	defer ms.lk.Unlock()

	// 调用 prep 方法进行准备操作
	if err := ms.prep(ctx); err != nil {
		// invalidate 在从 strmap 中删除此 peerMessageSender 之前调用。
		// 它可以防止peerMessageSender被重用/重新初始化然后被遗忘（保持流打开）。
		ms.invalidate() // 发生错误时使消息发送者无效
		return err
	}
	return nil
}

// prep 根据需要准备 peerMessageSender。
func (ms *peerMessageSender) prep(ctx context.Context) error {
	// 如果消息发送者已被标记为无效，则返回错误
	if ms.invalid {
		return fmt.Errorf("message sender has been invalidated")
	}
	// 如果网络流已存在，则无需准备
	if ms.s != nil {
		return nil
	}

	// 我们只想使用我们的主要协议与同行对话。
	// 我们不想查询任何只使用我们碰巧支持的辅助"服务器"协议之一的对等点（例如，出于向后兼容性原因，我们可以响应的旧节点）。
	nstr, err := ms.m.host.NewStream(ctx, ms.p, ms.m.protocols...) // 使用主机创建新的网络流
	if err != nil {
		return err
	}

	// NewVarintReaderSize 与 NewVarintReader 等效，但允许指定最大消息大小。
	ms.r = msgio.NewVarintReaderSize(nstr, network.MessageSizeMax) // 创建用于读取响应的读取器
	ms.s = nstr                                                    // 设置网络流

	return nil
}

// streamReuseTries 是我们在放弃并恢复到旧的每流一条消息行为之前尝试将流重用到给定对等点的次数。
const streamReuseTries = 3

// SendMessage 方法用于向对等点发送消息。
// 如果在发送消息时发生错误，将尝试重新使用流进行重试，最多重试 streamReuseTries 次。
// 如果重试次数超过 streamReuseTries，将关闭流并返回错误。
// 参数 ctx 是上下文对象，pmes 是要发送的消息。
// 返回发送消息过程中的错误，如果成功发送消息则返回 nil。
func (ms *peerMessageSender) SendMessage(ctx context.Context, pmes *pb.Message) error {
	if err := ms.lk.Lock(ctx); err != nil {
		return err
	}
	defer ms.lk.Unlock()

	// 是否重试标志
	retry := false
	for {
		// 准备消息发送者
		if err := ms.prep(ctx); err != nil {
			return err
		}

		// 写入消息
		if err := ms.writeMsg(pmes); err != nil {
			// Reset 关闭流的两端。 用它告诉远程端挂断电话并离开。
			_ = ms.s.Reset() // 重置流
			ms.s = nil

			if retry {
				logrus.Debug("error writing message", "error", err)
				return err
			}
			logrus.Debug("error writing message", "error", err, "retrying", true)
			// 设置重试标志为 true
			retry = true
			continue
		}

		var err error
		// 如果重试次数超过 streamReuseTries
		if ms.singleMes > streamReuseTries {
			// 关闭流
			err = ms.s.Close()
			ms.s = nil
		} else if retry {
			// 增加重试次数
			ms.singleMes++
		}

		return err
	}
}

// SendRequest 方法用于向对等点发送请求并等待响应。
// 如果在发送请求或接收响应时发生错误，将尝试重新使用流进行重试，最多重试 streamReuseTries 次。
// 如果重试次数超过 streamReuseTries，将关闭流并返回错误。
// 参数 ctx 是上下文对象，pmes 是要发送的请求消息。
// 返回接收到的响应消息和发送/接收过程中的错误，如果成功发送请求并接收到响应则返回 nil。
func (ms *peerMessageSender) SendRequest(ctx context.Context, pmes *pb.Message) (*pb.Message, error) {
	if err := ms.lk.Lock(ctx); err != nil {
		return nil, err
	}
	defer ms.lk.Unlock()

	// 是否重试标志
	retry := false
	for {
		// 准备消息发送者
		if err := ms.prep(ctx); err != nil {
			return nil, err
		}

		// 写入请求消息
		if err := ms.writeMsg(pmes); err != nil {
			// 重置流
			_ = ms.s.Reset()
			ms.s = nil

			if retry {
				logrus.Debug("error writing message", "error", err)
				return nil, err
			}
			logrus.Debug("error writing message", "error", err, "retrying", true)
			// 设置重试标志为 true
			retry = true
			continue
		}

		mes := new(pb.Message)
		// 从上下文中读取消息
		if err := ms.ctxReadMsg(ctx, mes); err != nil {
			// 重置流
			_ = ms.s.Reset()
			ms.s = nil

			if retry {
				logrus.Debug("error reading message", "error", err)
				return nil, err
			}
			logrus.Debug("error reading message", "error", err, "retrying", true)
			retry = true
			continue
		}

		var err error
		// 如果重试次数超过 streamReuseTries
		if ms.singleMes > streamReuseTries {
			// 关闭流
			err = ms.s.Close()
			ms.s = nil
		} else if retry {
			// 增加重试次数
			ms.singleMes++
		}

		return mes, err
	}
}

// writeMsg 方法将消息写入流中。
// 参数 pmes 是要写入的消息。
// 返回可能出现的错误。
func (ms *peerMessageSender) writeMsg(pmes *pb.Message) error {
	return WriteMsg(ms.s, pmes)
}

// ctxReadMsg 方法从上下文中读取消息。
// 参数 ctx 是上下文对象，mes 是用于存储读取到的消息的变量。
// 返回读取过程中的错误，如果成功读取消息则返回 nil。
func (ms *peerMessageSender) ctxReadMsg(ctx context.Context, mes *pb.Message) error {
	// 创建一个带有缓冲的通道 errc，用于接收读取过程中的错误
	errc := make(chan error, 1)
	// 启动一个 goroutine，执行以下操作
	go func(r msgio.ReadCloser) {
		// 在函数结束时关闭 errc 通道
		defer close(errc)
		// 从流中读取消息的字节和可能的错误
		bytes, err := r.ReadMsg()
		// 在函数结束时释放读取到的字节
		defer r.ReleaseMsg(bytes)
		if err != nil {
			// 如果读取过程中发生错误，将错误发送到 errc 通道
			errc <- err
			return
		}
		// 将读取到的字节解析为消息，并将解析过程中的错误发送到 errc 通道
		errc <- mes.Unmarshal(bytes)
	}(ms.r)

	// 创建一个定时器，用于设置读取超时时间
	t := time.NewTimer(dhtReadMessageTimeout)
	// 在函数结束时停止定时器
	defer t.Stop()

	select {
	case err := <-errc:
		// 返回读取过程中的错误
		return err
	case <-ctx.Done():
		// 如果上下文被取消，则返回上下文的错误
		return ctx.Err()
	case <-t.C:
		// 如果超过读取超时时间，则返回读取超时错误
		return ErrReadTimeout
	}
}

// Protobuf writer 在写入消息时执行多个小写入。
// 我们需要缓冲这些写入，以确保我们不会为每次写入发送新的数据包。
type bufferedDelimitedWriter struct {
	*bufio.Writer
	protoio.WriteCloser
}

// writerPool 是一个同步池，用于重用 bufferedDelimitedWriter 对象。
var writerPool = sync.Pool{
	New: func() interface{} {
		w := bufio.NewWriter(nil)
		return &bufferedDelimitedWriter{
			Writer:      w,
			WriteCloser: protoio.NewDelimitedWriter(w),
		}
	},
}

// WriteMsg 方法用于将消息写入到 io.Writer 接口中。
func WriteMsg(w io.Writer, mes *pb.Message) error {
	// 从 writerPool 中获取一个 bufferedDelimitedWriter 对象，并将其类型断言为 *bufferedDelimitedWriter。
	bw := writerPool.Get().(*bufferedDelimitedWriter)
	// 重置 bufferedDelimitedWriter 对象，将其绑定到指定的 io.Writer。
	bw.Reset(w)
	// 将消息写入到 bufferedDelimitedWriter 对象中。
	err := bw.WriteMsg(mes)
	if err == nil {
		// 刷新 bufferedDelimitedWriter 对象，将缓冲区中的数据写入到底层的 io.Writer。
		err = bw.Flush()
	}
	// 重置 bufferedDelimitedWriter 对象，将其解绑定。
	bw.Reset(nil)
	// 将 bufferedDelimitedWriter 对象放回 writerPool 中，以便重用。
	writerPool.Put(bw)
	// 返回可能发生的错误。
	return err
}

// Flush 方法用于刷新 bufferedDelimitedWriter 对象的缓冲区，将数据写入到底层的 io.Writer。
func (w *bufferedDelimitedWriter) Flush() error {
	// 刷新底层的 bufio.Writer 对象。
	return w.Writer.Flush()
}
