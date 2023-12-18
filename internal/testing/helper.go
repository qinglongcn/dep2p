package testing

import (
	"bytes"
	"fmt"
)

type TestValidator struct{}

func (TestValidator) Select(_ string, bs [][]byte) (int, error) {
	index := -1
	for i, b := range bs {
		if bytes.Equal(b, []byte("newer")) {
			index = i
		} else if bytes.Equal(b, []byte("valid")) {
			if index == -1 {
				index = i
			}
		}
	}
	if index == -1 {
		return -1, fmt.Errorf("no rec found")
	}
	return index, nil
}
func (TestValidator) Validate(_ string, b []byte) error {
	if bytes.Equal(b, []byte("expired")) {
		return fmt.Errorf("expired")
	}
	return nil
}
