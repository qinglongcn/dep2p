package streams

import (
	"bytes"
	"fmt"
	"io"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-msgio"
	"github.com/sirupsen/logrus"
)

// 表示消息头的长度
const (
	messageHeaderLen = 17
	MaxBlockSize     = 20000000 // 20M
)

var (
	messageHeader = headerSafe([]byte("/protobuf/msgio"))
)

// ReadStream 从流中读取消息
func ReadStream(stream network.Stream) ([]byte, error) {
	var header [messageHeaderLen]byte

	_, err := io.ReadFull(stream, header[:])
	if err != nil || !bytes.Equal(header[:], messageHeader) {
		logrus.Error("ReadStream", "pid", stream.Conn().RemotePeer().String(), "protocolID", stream.Protocol(), "read header err", err)
		return nil, err
	}

	reader := msgio.NewReaderSize(stream, MaxBlockSize)
	msg, err := reader.ReadMsg()
	if err != nil {
		logrus.Error("ReadStream", "pid", stream.Conn().RemotePeer().String(), "protocolID", stream.Protocol(), "read msg err", err)
		return nil, err
	}

	defer reader.ReleaseMsg(msg)

	return msg, nil
}

// WriteStream 将消息写入流
func WriteStream(msg []byte, stream network.Stream) error {
	_, err := stream.Write(messageHeader)
	if err != nil {
		logrus.Error("WriteStream", "pid", stream.Conn().RemotePeer().String(), "protocolID", stream.Protocol(), "write header err", err)
		return err
	}

	// NewWriter 包装了一个 io.Writer 和一个 msgio 框架的作者。 msgio.Writer 将写入每条消息的长度前缀。
	// writer := msgio.NewWriter(stream)
	// NewWriterWithPool 与 NewWriter 相同，但允许用户传递自定义缓冲池。
	writer := msgio.NewWriterWithPool(stream, pool.GlobalPool)

	// WriteMsg 将消息写入传入的缓冲区。
	if err = writer.WriteMsg(msg); err != nil {
		logrus.Error("WriteStream", "pid", stream.Conn().RemotePeer().String(), "protocolID", stream.Protocol(), "write msg err", err)
		return err
	}

	return nil
}

// CloseStream 写入后关闭流，并等待 EOF。
func CloseStream(stream network.Stream) {
	if stream == nil {
		return
	}

	// _ = stream.CloseWrite()
	// _ = stream.CloseRead()

	go func() {
		// AwaitEOF 等待给定流上的 EOF，如果失败则返回错误。 它最多等待 EOFTimeout（默认为 1 分钟），然后重置流。
		err := AwaitEOF(stream)
		if err != nil {
			// 只是记录它，因为这无关紧要
			logrus.Debug("CloseStream", "err", err, "protocol ID", stream.Protocol())
		}
	}()
}

// HandlerWithClose 用关闭流和从恐慌中恢复来包装处理程序
func HandlerWithClose(f network.StreamHandler) network.StreamHandler {
	return func(stream network.Stream) {
		defer func() {
			// recover 内置函数允许程序管理恐慌 goroutine 的行为。
			// 在延迟函数（但不是它调用的任何函数）内执行恢复调用，通过恢复正常执行来停止恐慌序列，并检索传递给恐慌调用的错误值。
			// 如果在延迟函数之外调用 recover，它不会停止恐慌序列。
			// 在这种情况下，或者当 goroutine 没有 panic 时，或者如果提供给 panic 的参数是 nil，recover 返回 nil。
			// 因此 recover 的返回值报告了 goroutine 是否 panicing。
			if r := recover(); r != nil {
				logrus.Error("handle stream", "panic error", r)
				fmt.Println(string(panicTrace(4)))
				// 关闭流的两端。 用它来告诉远端挂断电话并离开。
				_ = stream.Reset()
			}
		}()
		f(stream)
		// 写入后关闭流，并等待 EOF。
		CloseStream(stream)
	}
}

// HandlerWithWrite 通过写入、关闭流和从恐慌中恢复来包装处理程序
func HandlerWithWrite(f func(request *RequestMessage) error) network.StreamHandler {
	return func(stream network.Stream) {
		var req RequestMessage
		if err := f(&req); err != nil {
			return
		}

		// 序列化请求
		requestByte, err := req.Marshal()
		if err != nil {
			return
		}

		// WriteStream 将消息写入流。
		if err := WriteStream(requestByte, stream); err != nil {
			return
		}
	}
}

// HandlerWithRead 用读取、关闭流和从恐慌中恢复来包装处理程序
func HandlerWithRead(f func(request *RequestMessage)) network.StreamHandler {
	return func(stream network.Stream) {

		var req RequestMessage

		// ReadStream 从流中读取消息。
		requestByte, err := ReadStream(stream)
		if err != nil {
			return
		}
		if err := req.Unmarshal(requestByte); err != nil {
			return
		}
		// // 反序列化请求
		// req, err := utils.DeserializeRequest(requestByte)
		// if err != nil {
		// 	return
		// }

		f(&req)
	}
}

// HandlerWithRW 用读取、写入、关闭流和从恐慌中恢复来包装处理程序
func HandlerWithRW(f func(request *RequestMessage, response *ResponseMessage) error) network.StreamHandler {
	// 返回一个网络流处理器函数
	return func(stream network.Stream) {

		var req RequestMessage
		var res ResponseMessage
		res.Code = 201
		res.Message = &Message{
			Sender: stream.Conn().LocalPeer().String(), // 发送方ID
		}
		//res.Message.Sender = stream.Conn().LocalPeer().String()

		// 从流中读取消息
		requestByte, err := ReadStream(stream)
		if err != nil {
			res.Msg = "请求无法处理" // 响应消息

		} else if len(requestByte) == 0 {
			return
		} else if len(requestByte) > 0 {
			if err := req.Unmarshal(requestByte); err != nil {
				// 处理请求解析错误
				res.Msg = "请求解析错误"
			} else {
				// 调用处理函数处理请求并获取响应
				if err := f(&req, &res); err != nil {
					res.Msg = err.Error()
				}
			}
		}

		responseByte, err := res.Marshal()
		if err != nil {
			return
		}
		// 将响应消息写入流中
		if err := WriteStream(responseByte, stream); err != nil {
			return
		}
	}
}
