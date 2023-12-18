package dep2ptls

import (
	"crypto/tls"

	ci "github.com/bpfs/dep2p/core/crypto"
	"github.com/bpfs/dep2p/core/network"
	"github.com/bpfs/dep2p/core/peer"
	"github.com/bpfs/dep2p/core/sec"
)

type conn struct {
	*tls.Conn

	localPeer       peer.ID
	remotePeer      peer.ID
	remotePubKey    ci.PubKey
	connectionState network.ConnectionState
}

var _ sec.SecureConn = &conn{}

func (c *conn) LocalPeer() peer.ID {
	return c.localPeer
}

func (c *conn) RemotePeer() peer.ID {
	return c.remotePeer
}

func (c *conn) RemotePublicKey() ci.PubKey {
	return c.remotePubKey
}

func (c *conn) ConnState() network.ConnectionState {
	return c.connectionState
}
