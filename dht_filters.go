// kad-dht

package dep2p

import (
	dhtcfg "github.com/bpfs/dep2p/internal/config"
)

// QueryFilterFunc 是在查询时考虑要拨号的对等点时应用的过滤器
type QueryFilterFunc = dhtcfg.QueryFilterFunc

// RouteTableFilterFunc 是在考虑要保留在本地路由表中的连接时应用的过滤器。
type RouteTableFilterFunc = dhtcfg.RouteTableFilterFunc
