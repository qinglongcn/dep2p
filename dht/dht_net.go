package dht

import (
	"io"
	"time"

	"github.com/bpfs/dep2p/core/network"
	"github.com/sirupsen/logrus"

	"github.com/bpfs/dep2p/dht/internal/net"
	"github.com/bpfs/dep2p/dht/metrics"
	pb "github.com/bpfs/dep2p/dht/pb"

	"github.com/bpfs/dep2p/util/msgio"
	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
)

var dhtStreamIdleTimeout = 1 * time.Minute

// ErrReadTimeout is an error that occurs when no message is read within the timeout period.
var ErrReadTimeout = net.ErrReadTimeout

// handleNewStream implements the network.StreamHandler
func (dht *DeP2PDHT) handleNewStream(s network.Stream) {
	if dht.handleNewMessage(s) {
		// If we exited without error, close gracefully.
		_ = s.Close()
	} else {
		// otherwise, send an error.
		_ = s.Reset()
	}
}

// Returns true on orderly completion of writes (so we can Close the stream).
func (dht *DeP2PDHT) handleNewMessage(s network.Stream) bool {
	ctx := dht.ctx
	r := msgio.NewVarintReaderSize(s, network.MessageSizeMax)

	mPeer := s.Conn().RemotePeer()

	timer := time.AfterFunc(dhtStreamIdleTimeout, func() { _ = s.Reset() })
	defer timer.Stop()

	for {
		if dht.getMode() != modeServer {
			logrus.Debugf("ignoring incoming dht message while not in server mode")
			return false
		}

		var req pb.Message
		msgbytes, err := r.ReadMsg()
		msgLen := len(msgbytes)
		if err != nil {
			r.ReleaseMsg(msgbytes)
			if err == io.EOF {
				return true
			}
			// This string test is necessary because there isn't a single stream reset error
			// instance	in use.
			if logrus.IsLevelEnabled(logrus.DebugLevel) && err.Error() != "stream reset" {
				logrus.WithFields(logrus.Fields{
					"from": mPeer.String(),
					"err":  err,
				}).Debug("error reading message")
			}

			if msgLen > 0 {
				_ = stats.RecordWithTags(ctx,
					[]tag.Mutator{tag.Upsert(metrics.KeyMessageType, "UNKNOWN")},
					metrics.ReceivedMessages.M(1),
					metrics.ReceivedMessageErrors.M(1),
					metrics.ReceivedBytes.M(int64(msgLen)),
				)
			}
			return false
		}
		err = req.Unmarshal(msgbytes)
		r.ReleaseMsg(msgbytes)
		if err != nil {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"from": mPeer.String(),
					"err":  err,
				}).Debug("error unmarshaling message")
			}
			_ = stats.RecordWithTags(ctx,
				[]tag.Mutator{tag.Upsert(metrics.KeyMessageType, "UNKNOWN")},
				metrics.ReceivedMessages.M(1),
				metrics.ReceivedMessageErrors.M(1),
				metrics.ReceivedBytes.M(int64(msgLen)),
			)
			return false
		}

		timer.Reset(dhtStreamIdleTimeout)

		startTime := time.Now()
		ctx, _ := tag.New(ctx,
			tag.Upsert(metrics.KeyMessageType, req.GetType().String()),
		)

		stats.Record(ctx,
			metrics.ReceivedMessages.M(1),
			metrics.ReceivedBytes.M(int64(msgLen)),
		)

		handler := dht.handlerForMsgType(req.GetType())
		if handler == nil {
			stats.Record(ctx, metrics.ReceivedMessageErrors.M(1))
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"from": mPeer.String(),
					"type": req.GetType(),
				}).Debug("can't handle received message")
			}
			return false
		}

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"from": mPeer.String(),
				"type": req.GetType(),
				"key":  req.GetKey(),
			}).Debug("handling message")
		}
		resp, err := handler(ctx, mPeer, &req)
		if err != nil {
			stats.Record(ctx, metrics.ReceivedMessageErrors.M(1))
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"from": mPeer.String(),
					"type": req.GetType(),
					"key":  req.GetKey(),
					"err":  err,
				}).Debug("error handling message")
			}
			return false
		}

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"from": mPeer.String(),
				"type": req.GetType(),
				"key":  req.GetKey(),
			}).Debug("handled message")
		}

		if resp == nil {
			continue
		}

		// send out response msg
		err = net.WriteMsg(s, resp)
		if err != nil {
			stats.Record(ctx, metrics.ReceivedMessageErrors.M(1))
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"from": mPeer.String(),
					"type": req.GetType(),
					"key":  req.GetKey(),
					"err":  err,
				}).Debug("error writing response")
			}
			return false
		}

		elapsedTime := time.Since(startTime)

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"from": mPeer.String(),
				"type": req.GetType(),
				"key":  req.GetKey(),
				"err":  err,
			}).Debug("responded to message")
		}

		latencyMillis := float64(elapsedTime) / float64(time.Millisecond)
		stats.Record(ctx, metrics.InboundRequestLatency.M(latencyMillis))
	}
}
