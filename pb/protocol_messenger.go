package dht_pb

import (
	"bytes"
	"context"
	"fmt"

	recpb "github.com/libp2p/go-libp2p-record/pb"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bpfs/dep2p/internal"
)

// ProtocolMessenger 可用于向对等方发送 DHT 消息并处理他们的响应。
// 这将有线协议格式与 DHT 协议实现和routing.Routing 接口的实现解耦。
//
// 注意：ProtocolMessenger 的 MessageSender 仍然需要处理一些有线协议细节，例如使用 varint 描述的 protobuf
type ProtocolMessenger struct {
	m MessageSender
}

type ProtocolMessengerOption func(*ProtocolMessenger) error

// NewProtocolMessenger 创建一个新的 ProtocolMessenger，用于向对等方发送 DHT 消息并处理他们的响应。
func NewProtocolMessenger(msgSender MessageSender, opts ...ProtocolMessengerOption) (*ProtocolMessenger, error) {
	pm := &ProtocolMessenger{
		m: msgSender,
	}

	for _, o := range opts {
		if err := o(pm); err != nil {
			return nil, err
		}
	}

	return pm, nil
}

type MessageSenderWithDisconnect interface {
	MessageSender

	OnDisconnect(context.Context, peer.ID)
}

// MessageSender 处理向给定对等方发送有线协议消息
type MessageSender interface {
	// SendRequest 向对等方发送消息并等待其响应
	SendRequest(ctx context.Context, p peer.ID, pmes *Message) (*Message, error)
	// SendMessage 向对等方发送消息而不等待响应
	SendMessage(ctx context.Context, p peer.ID, pmes *Message) error
}

// PutValue 要求对等方存储给定的键/值对。
func (pm *ProtocolMessenger) PutValue(ctx context.Context, p peer.ID, rec *recpb.Record) (err error) {
	ctx, span := internal.StartSpan(ctx, "ProtocolMessenger.PutValue")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(attribute.Stringer("to", p), attribute.Stringer("record", rec))
		defer func() {
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
			}
		}()
	}

	pmes := NewMessage(Message_PUT_VALUE, rec.Key, nil, 0)
	pmes.Record = rec
	rpmes, err := pm.m.SendRequest(ctx, p, pmes)
	if err != nil {
		logrus.Debugln("failed to put value to peer", "to", p, "key", internal.LoggableRecordKeyBytes(rec.Key), "error", err)
		return err
	}

	if !bytes.Equal(rpmes.GetRecord().Value, pmes.GetRecord().Value) {
		const errStr = "value not put correctly"
		logrus.Infoln(errStr, "put-message", pmes, "get-message", rpmes)
		return fmt.Errorf(errStr)
	}

	return nil
}

// GetValue 向对等方询问与给定键对应的值。
// 还返回与该键最接近的 K 个对等点，如 GetClosestPeers 中所述。
func (pm *ProtocolMessenger) GetValue(ctx context.Context, p peer.ID, key string) (record *recpb.Record, closerPeers []*peer.AddrInfo, err error) {
	ctx, span := internal.StartSpan(ctx, "ProtocolMessenger.GetValue")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(attribute.Stringer("to", p), internal.KeyAsAttribute("key", key))
		defer func() {
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
			} else {
				peers := make([]string, len(closerPeers))
				for i, v := range closerPeers {
					peers[i] = v.String()
				}
				span.SetAttributes(
					attribute.Stringer("record", record),
					attribute.StringSlice("closestPeers", peers),
				)
			}
		}()
	}

	pmes := NewMessage(Message_GET_VALUE, []byte(key), nil, 0)
	respMsg, err := pm.m.SendRequest(ctx, p, pmes)
	if err != nil {
		return nil, nil, err
	}

	// 也许我们被赋予了更亲密的同龄人
	peers := PBPeersToPeerInfos(respMsg.GetCloserPeers())

	if rec := respMsg.GetRecord(); rec != nil {
		// 成功！ 我们被赋予了价值
		logrus.Debug("got value")

		// 检查记录是否与我们要查找的记录匹配（此处不会验证记录）
		if !bytes.Equal([]byte(key), rec.GetKey()) {
			logrus.Debug("received incorrect record")
			return nil, nil, internal.ErrIncorrectRecord
		}

		return rec, peers, err
	}

	return nil, peers, nil
}

// GetClosestPeers 要求对等点返回 XOR 空间中最接近 id 的 K（DHT 范围参数）DHT 服务器对等点
// 注意：如果peer碰巧知道另一个peerID与给定id完全匹配的peer，它将返回该peer，即使该peer不是DHT服务器节点。
func (pm *ProtocolMessenger) GetClosestPeers(ctx context.Context, p peer.ID, id peer.ID, value []byte) (closerPeers []*peer.AddrInfo, err error) {
	// StartSpan 函数用于启动一个跨度（span）。
	// 它接收一个上下文（context.Context）对象、一个跨度名称（name）和一些跨度启动选项（opts）作为输入参数。 返回更新后的上下文对象和一个跨度对象。
	ctx, span := internal.StartSpan(ctx, "ProtocolMessenger.GetClosestPeers")
	// End 完成了 Span。
	// 调用此方法后，Span 被认为是完整的，并准备好通过遥测管道的其余部分进行传递。 因此，调用此方法后不允许更新 Span。
	defer span.End()

	// IsRecording 返回 Span 的录音状态。
	// 如果 Span 处于活动状态并且可以记录事件，它将返回 true。
	if span.IsRecording() {
		// SetAttributes 将 kv 设置为 Span 的属性。
		// 如果 kv 中的键已经存在于 Span 的属性中，它将被 kv 中包含的值覆盖。
		span.SetAttributes(attribute.Stringer("to", p), attribute.Stringer("key", id))
		defer func() {
			if err != nil {
				// SetStatus 以代码和描述的形式设置 Span 的状态，前提是状态之前尚未设置为更高的值（确定 > 错误 > 取消设置）。
				// 仅当代码表示错误时，描述才会包含在状态中。
				span.SetStatus(codes.Error, err.Error())
			} else {
				peers := make([]string, len(closerPeers))
				for i, v := range closerPeers {
					peers[i] = v.String()
				}
				// SetAttributes 将 kv 设置为 Span 的属性。
				// 如果 kv 中的键已经存在于 Span 的属性中，它将被 kv 中包含的值覆盖。
				span.SetAttributes(attribute.StringSlice("peers", peers))
			}
		}()
	}

	// NewMessage 构造一个具有给定类型、键和级别的新的 DHT 消息
	pmes := NewMessage(Message_FIND_NODE, []byte(id), value, 0)
	// SendRequest 向对等方发送消息并等待其响应
	respMsg, err := pm.m.SendRequest(ctx, p, pmes)
	if err != nil {
		return nil, err
	}
	// PBPeersToPeerInfos 将给定的 []*Message_Peer 转换为 []peer.AddrInfo 无效的地址将被静默忽略。
	peers := PBPeersToPeerInfos(respMsg.GetCloserPeers())
	return peers, nil
}

// PutProvider 要求对等方存储我们是给定密钥的提供者。
func (pm *ProtocolMessenger) PutProvider(ctx context.Context, p peer.ID, key multihash.Multihash, host host.Host) (err error) {
	ctx, span := internal.StartSpan(ctx, "ProtocolMessenger.PutProvider")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(attribute.Stringer("to", p), attribute.Stringer("key", key))
		defer func() {
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
			}
		}()
	}

	pi := peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}

	// TODO: 我们可能希望限制提供商记录中的地址类型
	// 例如，在仅限 WAN 的 DHT 中禁止共享非 WAN 地址（例如 192.168.0.100）
	if len(pi.Addrs) < 1 {
		return fmt.Errorf("no known addresses for self, cannot put provider")
	}

	pmes := NewMessage(Message_ADD_PROVIDER, key, nil, 0)
	pmes.ProviderPeers = RawPeerInfosToPBPeers([]peer.AddrInfo{pi})

	return pm.m.SendMessage(ctx, p, pmes)
}

// GetProviders 向对等方询问其所知道的给定密钥的提供者。 还返回与该键最接近的 K 个对等点，如 GetClosestPeers 中所述。
func (pm *ProtocolMessenger) GetProviders(ctx context.Context, p peer.ID, key multihash.Multihash) (provs []*peer.AddrInfo, closerPeers []*peer.AddrInfo, err error) {
	ctx, span := internal.StartSpan(ctx, "ProtocolMessenger.GetProviders")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(attribute.Stringer("to", p), attribute.Stringer("key", key))
		defer func() {
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
			} else {
				provsStr := make([]string, len(provs))
				for i, v := range provs {
					provsStr[i] = v.String()
				}
				closerPeersStr := make([]string, len(provs))
				for i, v := range closerPeers {
					closerPeersStr[i] = v.String()
				}
				span.SetAttributes(attribute.StringSlice("provs", provsStr), attribute.StringSlice("closestPeers", closerPeersStr))
			}
		}()
	}

	pmes := NewMessage(Message_GET_PROVIDERS, key, nil, 0)
	respMsg, err := pm.m.SendRequest(ctx, p, pmes)
	if err != nil {
		return nil, nil, err
	}
	provs = PBPeersToPeerInfos(respMsg.GetProviderPeers())
	closerPeers = PBPeersToPeerInfos(respMsg.GetCloserPeers())
	return provs, closerPeers, nil
}

// Ping 向传递的对等方发送 ping 消息并等待响应。
func (pm *ProtocolMessenger) Ping(ctx context.Context, p peer.ID) (err error) {
	ctx, span := internal.StartSpan(ctx, "ProtocolMessenger.Ping")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(attribute.Stringer("to", p))
		defer func() {
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
			}
		}()
	}

	req := NewMessage(Message_PING, nil, nil, 0)
	resp, err := pm.m.SendRequest(ctx, p, req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	if resp.Type != Message_PING {
		return fmt.Errorf("got unexpected response type: %v", resp.Type)
	}
	return nil
}
