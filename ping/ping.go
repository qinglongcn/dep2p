package ping

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	mrand "math/rand"
	"time"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
)

const (
	PingSize    = 32                 // PingSize 定义了 Ping 数据包的大小为 32 字节。
	pingTimeout = time.Second * 60   // pingTimeout 定义了 Ping 超时时间为 60 秒。
	ID          = "/dep2p/ping/1.0.0" // ID 定义了 Ping 服务的协议标识为 "/dep2p/ping/1.0.0"。
	ServiceName = "dep2p.ping"      // ServiceName 定义了 Ping 服务的名称为 "dep2p.ping"。
)

// PingService 是一个实现了 dep2p.ping 协议的服务
type PingService struct {
	Host host.Host // Host表示 Ping 服务所在的主机。
}

// NewPingService 是创建 PingService 实例的函数
// 它接受一个 host.Host 参数 h，并返回一个指向 PingService 的指针
func NewPingService(h host.Host) *PingService {
	// 创建一个 PingService 实例 ps，并将参数 h 赋值给它的 Host 字段
	ps := &PingService{h}
	// 设置流处理器，当收到指定协议标识的流时，调用 PingHandler 方法进行处理
	h.SetStreamHandler(ID, ps.PingHandler)
	// 返回 PingService 实例的指针
	return ps
}

// PingHandler 是 PingService 的 Ping 处理方法
// 它接受一个 network.Stream 参数 s，用于处理 Ping 请求
func (p *PingService) PingHandler(s network.Stream) {
	// 将流 s 附加到 Ping 服务的服务范围中
	if err := s.Scope().SetService(ServiceName); err != nil {
		logrus.Debugf("将流附加到 Ping 服务时出错：%s", err)
		s.Reset()
		return
	}

	// 为 Ping 流预留内存
	if err := s.Scope().ReserveMemory(PingSize, network.ReservationPriorityAlways); err != nil {
		logrus.Debugf("为 Ping 流预留内存时出错：%s", err)
		s.Reset()
		return
	}
	defer s.Scope().ReleaseMemory(PingSize)

	// 从对象池中获取缓冲区
	buf := pool.Get(PingSize)
	defer pool.Put(buf)

	// 创建一个用于传递错误的通道
	errCh := make(chan error, 1)
	defer close(errCh)
	// 创建一个定时器，用于检测 Ping 的超时
	timer := time.NewTimer(pingTimeout)
	defer timer.Stop()

	// 启动一个 goroutine，用于处理超时和错误
	go func() {
		select {
		case <-timer.C:
			logrus.Debug("Ping 超时")
		case err, ok := <-errCh:
			if ok {
				logrus.Debug(err)
			} else {
				logrus.Errorln("Ping 循环失败，没有错误")
			}
		}
		s.Close()
	}()

	// 循环处理 Ping 请求
	for {
		// 从流中读取数据到缓冲区
		_, err := io.ReadFull(s, buf)
		if err != nil {
			errCh <- err
			return
		}

		// 将缓冲区的数据写入流
		_, err = s.Write(buf)
		if err != nil {
			errCh <- err
			return
		}

		// 重置定时器，以防止 Ping 超时
		timer.Reset(pingTimeout)
	}
}

// Result 是一个 Ping 尝试的结果，可以是往返时间（RTT）或错误。
type Result struct {
	RTT   time.Duration // RTT 字段表示往返时间（Round-Trip Time），即从发送 Ping 请求到接收到响应的时间间隔。
	Error error         // Error 字段表示在 Ping 过程中可能发生的错误。
}

// Ping 是 PingService 的 Ping 方法，用于向指定的对等节点发送 Ping 请求，并返回一个结果通道。
// 它接受一个上下文对象 ctx，用于控制 Ping 操作的取消或超时。
// 参数 p 是要 Ping 的对等节点的 peer.ID。
// 返回一个只读的结果通道，用于接收 Ping 操作的结果。
func (ps *PingService) Ping(ctx context.Context, p peer.ID) <-chan Result {
	// 调用 Ping 函数，将 PingService 的 Host 和目标对等节点的 peer.ID 作为参数传递
	return Ping(ctx, ps.Host, p)
}

// pingError 是一个辅助函数，用于创建一个包含错误结果的结果通道。
// 它接受一个错误 err，并返回一个只包含该错误结果的通道。
func pingError(err error) chan Result {
	ch := make(chan Result, 1)
	ch <- Result{Error: err}
	close(ch)
	return ch
}

// Ping 是一个 Ping 方法，用于向远程对等节点发送 Ping 请求，直到上下文被取消，返回一个包含往返时间（RTT）或错误的结果流。
func Ping(ctx context.Context, h host.Host, p peer.ID) <-chan Result {
	// 使用 host.Host 的 NewStream 方法创建一个新的流，用于发送 Ping 请求
	s, err := h.NewStream(network.WithUseTransient(ctx, "ping"), p, ID)
	if err != nil {
		// 如果创建流时发生错误，返回一个包含该错误的结果通道
		return pingError(err)
	}

	// 将流附加到 ping 服务
	if err := s.Scope().SetService(ServiceName); err != nil {
		logrus.Debugf("将流附加到 ping 服务时发生错误：%s", err)
		s.Reset()
		return pingError(err)
	}

	// 创建一个包含 8 个字节的字节数组，并使用随机数填充
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		logrus.Errorf("获取加密随机数时发生错误：%s", err)
		s.Reset()
		return pingError(err)
	}
	// 使用字节数组生成随机数生成器
	ra := mrand.New(mrand.NewSource(int64(binary.BigEndian.Uint64(b))))

	// 使用 context.WithCancel 创建一个新的上下文对象和取消函数
	ctx, cancel := context.WithCancel(ctx)

	// 创建一个结果通道
	out := make(chan Result)
	go func() {
		defer close(out)
		defer cancel()

		// 在上下文未被取消的情况下循环执行 Ping 操作
		for ctx.Err() == nil {
			var res Result
			// 调用 ping 函数进行 Ping 操作，并将结果保存到 res 变量中
			res.RTT, res.Error = ping(s, ra)

			// 如果上下文已被取消，则忽略所有操作
			if ctx.Err() != nil {
				return
			}

			// 如果没有错误，记录往返时间（RTT）
			if res.Error == nil {
				h.Peerstore().RecordLatency(p, res.RTT)
			}

			select {
			case out <- res:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		// 强制中止 Ping 操作
		<-ctx.Done()
		s.Reset()
	}()

	return out
}

// ping 是一个 ping 函数，用于执行 Ping 操作并返回往返时间（RTT）和错误。
func ping(s network.Stream, randReader io.Reader) (time.Duration, error) {
	// 为 ping 流保留内存空间
	if err := s.Scope().ReserveMemory(2*PingSize, network.ReservationPriorityAlways); err != nil {
		logrus.Debugf("为 ping 流保留内存空间时发生错误：%s", err)
		s.Reset()
		return 0, err
	}
	defer s.Scope().ReleaseMemory(2 * PingSize)

	// 从池中获取一个大小为 PingSize 的缓冲区
	buf := pool.Get(PingSize)
	defer pool.Put(buf)

	// 从随机数读取器中读取随机数据填充缓冲区
	if _, err := io.ReadFull(randReader, buf); err != nil {
		return 0, err
	}

	// 记录当前时间
	before := time.Now()
	// 将缓冲区的数据写入流
	if _, err := s.Write(buf); err != nil {
		return 0, err
	}

	// 从池中获取一个大小为 PingSize 的缓冲区
	rbuf := pool.Get(PingSize)
	defer pool.Put(rbuf)

	// 从流中读取数据填充接收缓冲区
	if _, err := io.ReadFull(s, rbuf); err != nil {
		return 0, err
	}

	// 检查发送和接收的数据是否相等
	if !bytes.Equal(buf, rbuf) {
		return 0, fmt.Errorf("ping 数据包不正确")
	}

	// 返回从发送到接收的时间间隔和 nil 错误
	return time.Since(before), nil
}
