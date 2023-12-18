package tlsdiag

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"time"

	dep2ptls "github.com/bpfs/dep2p/p2p/security/tls"

	"github.com/bpfs/dep2p/core/peer"
)

func StartClient() error {
	port := flag.Int("p", 5533, "port")
	peerIDString := flag.String("id", "", "peer ID")
	keyType := flag.String("key", "ecdsa", "rsa, ecdsa, ed25519 or secp256k1")
	flag.Parse()

	priv, err := generateKey(*keyType)
	if err != nil {
		return err
	}

	peerID, err := peer.Decode(*peerIDString)
	if err != nil {
		return err
	}

	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return err
	}
	fmt.Printf(" Peer ID: %s\n", id)
	tp, err := dep2ptls.New(dep2ptls.ID, priv, nil)
	if err != nil {
		return err
	}

	remoteAddr := fmt.Sprintf("localhost:%d", *port)
	fmt.Printf("Dialing %s\n", remoteAddr)
	conn, err := net.Dial("tcp", remoteAddr)
	if err != nil {
		return err
	}
	fmt.Printf("Dialed raw connection to %s\n", conn.RemoteAddr())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sconn, err := tp.SecureOutbound(ctx, conn, peerID)
	if err != nil {
		return err
	}
	fmt.Printf("Authenticated server: %s\n", sconn.RemotePeer())
	data, err := io.ReadAll(sconn)
	if err != nil {
		return err
	}
	fmt.Printf("Received message from server: %s\n", string(data))
	return nil
}
