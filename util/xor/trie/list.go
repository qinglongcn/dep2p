package trie

import (
	"github.com/bpfs/dep2p/util/xor/key"
)

// List returns a list of all keys in the trie.
func (trie *Trie) List() []key.Key {
	switch {
	case trie.IsEmptyLeaf():
		return nil
	case trie.IsNonEmptyLeaf():
		return []key.Key{trie.Key}
	default:
		return append(trie.Branch[0].List(), trie.Branch[1].List()...)
	}
}
