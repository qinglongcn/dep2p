package kbucket

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/test"
	"github.com/stretchr/testify/require"
)

// TestCloser 是一个测试函数，用于测试 Closer() 方法的行为
func TestCloser(t *testing.T) {
	Pa := test.RandPeerIDFatal(t)
	Pb := test.RandPeerIDFatal(t)
	var X string

	// 如果 d(Pa, X) < d(Pb, X)，则返回 true
	for {
		X = string(test.RandPeerIDFatal(t))
		if xor(ConvertPeerID(Pa), ConvertKey(X)).less(xor(ConvertPeerID(Pb), ConvertKey(X))) {
			break
		}
	}

	// 断言 Closer(Pa, Pb, X) 返回 true
	require.True(t, Closer(Pa, Pb, X))

	// 如果 d(Pa, X) > d(Pb, X)，则返回 false
	for {
		X = string(test.RandPeerIDFatal(t))
		if xor(ConvertPeerID(Pb), ConvertKey(X)).less(xor(ConvertPeerID(Pa), ConvertKey(X))) {
			break
		}
	}

	// 断言 Closer(Pa, Pb, X) 返回 false
	require.False(t, Closer(Pa, Pb, X))
}
