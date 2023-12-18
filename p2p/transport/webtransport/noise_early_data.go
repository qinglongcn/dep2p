package dep2pwebtransport

import (
	"context"
	"net"

	"github.com/bpfs/dep2p/core/peer"
	"github.com/bpfs/dep2p/p2p/security/noise"
	"github.com/bpfs/dep2p/p2p/security/noise/pb"
)

type earlyDataHandler struct {
	earlyData *pb.NoiseExtensions
	receive   func(extensions *pb.NoiseExtensions) error
}

var _ noise.EarlyDataHandler = &earlyDataHandler{}

func newEarlyDataSender(earlyData *pb.NoiseExtensions) noise.EarlyDataHandler {
	return &earlyDataHandler{earlyData: earlyData}
}

func newEarlyDataReceiver(receive func(*pb.NoiseExtensions) error) noise.EarlyDataHandler {
	return &earlyDataHandler{receive: receive}
}

func (e *earlyDataHandler) Send(context.Context, net.Conn, peer.ID) *pb.NoiseExtensions {
	return e.earlyData
}

func (e *earlyDataHandler) Received(_ context.Context, _ net.Conn, ext *pb.NoiseExtensions) error {
	if e.receive == nil {
		return nil
	}
	return e.receive(ext)
}
