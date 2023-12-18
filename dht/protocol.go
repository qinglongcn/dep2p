package dht

import (
	"github.com/bpfs/dep2p/core/protocol"
)

var (
	// ProtocolDHT is the default DHT protocol.
	ProtocolDHT protocol.ID = "/dep2p/kad/1.0.0"
	// DefaultProtocols spoken by the DHT.
	DefaultProtocols = []protocol.ID{ProtocolDHT}
)
