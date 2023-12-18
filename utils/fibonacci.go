package utils

// FibonacciArray create a fibonacci array with length n.
func FibonacciArray(n int) []int64 {
	res := make([]int64, n)
	for i := 0; i < n; i++ {
		if i <= 1 {
			res[i] = 1
		} else {
			res[i] = res[i-1] + res[i-2]
		}
	}
	return res
}
