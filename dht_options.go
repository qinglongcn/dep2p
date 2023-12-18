// kad-dht

package dep2p

import (
	dhtcfg "github.com/bpfs/dep2p/internal/config"
)

// ModeOpt describes what mode the dht should operate in
type ModeOpt = dhtcfg.ModeOpt

const (
	// ModeAuto utilizes EvtLocalReachabilityChanged events sent over the event bus to dynamically switch the DHT
	// between Client and Server modes based on network conditions
	ModeAuto ModeOpt = iota
	// ModeClient operates the DHT as a client only, it cannot respond to incoming queries
	ModeClient
	// ModeServer operates the DHT as a server, it can both send and respond to queries
	ModeServer
	// ModeAutoServer operates in the same way as ModeAuto, but acts as a server when reachability is unknown
	ModeAutoServer
)

type Option = dhtcfg.Option

// Mode 配置 DHT 运行的模式（客户端、服务器、自动）。
//
// 默认为自动模式。
func Mode(m ModeOpt) Option {
	return func(c *dhtcfg.Config) error {
		c.Mode = m
		return nil
	}
}
