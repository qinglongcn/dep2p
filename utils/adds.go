/*
Copyright (C) BABEC. All rights reserved.

SPDX-License-Identifier: Apache-2.0
*/

package utils

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ParseMultiAddrs parse multi addr string to multiaddr.Multiaddr .
func ParseMultiAddrs(addrs []string) ([]multiaddr.Multiaddr, error) {
	var mutiAddrs = make([]multiaddr.Multiaddr, 0, len(addrs))
	if len(addrs) > 0 {
		for _, addr := range addrs {
			ma, err := multiaddr.NewMultiaddr(addr)
			if err != nil {
				return nil, err
			}
			mutiAddrs = append(mutiAddrs, ma)
		}
	}
	return mutiAddrs, nil
}

// ParseAddrInfo parse multi addr string to peer.AddrInfo .
func ParseAddrInfo(addrs []string) ([]peer.AddrInfo, error) {
	ais := make([]peer.AddrInfo, 0)
	mas, err := ParseMultiAddrs(addrs)
	if err != nil {
		return nil, err
	}
	for _, peerAddr := range mas {
		pif, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			return nil, err
		}
		ais = append(ais, *pif)
	}
	return ais, nil
}
