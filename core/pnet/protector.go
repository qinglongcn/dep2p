// Package pnet provides interfaces for private networking in dep2p.
package pnet

// A PSK enables private network implementation to be transparent in dep2p.
// It is used to ensure that peers can only establish connections to other peers
// that are using the same PSK.
type PSK []byte
