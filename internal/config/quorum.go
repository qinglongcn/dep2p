package config

import "github.com/libp2p/go-libp2p/core/routing"

type QuorumOptionKey struct{}

const defaultQuorum = 0

// GetQuorum 方法用于获取选项中的 Quorum 值，如果找不到选项，则返回默认值。
func GetQuorum(opts *routing.Options) int {
	// 从选项中获取 Quorum 值，如果没有找到选项，GetQuorum 默认为 0
	responsesNeeded, ok := opts.Other[QuorumOptionKey{}].(int)
	if !ok {
		// 如果选项中没有 Quorum 值，则使用默认值
		responsesNeeded = defaultQuorum
	}
	return responsesNeeded
}
