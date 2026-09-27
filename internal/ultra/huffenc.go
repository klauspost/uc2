package ultra

import "slices"

// huffLengths computes length-limited Huffman code lengths for freq into lens.
// The code is always complete. A single used symbol s gets {s, (s+1)%n} of
// length 1, as UC2 does; no used symbols gives {0, 1}.
func huffLengths(freq []uint32, maxLen int, lens []byte) {
	n := len(freq)
	clear(lens[:n])
	var syms [nSym]uint16
	used := syms[:0]
	for s, f := range freq {
		if f > 0 {
			used = append(used, uint16(s))
		}
	}
	switch len(used) {
	case 0:
		lens[0], lens[1] = 1, 1
		return
	case 1:
		s := int(used[0])
		lens[s], lens[(s+1)%n] = 1, 1
		return
	}
	slices.SortStableFunc(used, func(a, b uint16) int { return int(freq[a]) - int(freq[b]) })

	// Two-queue Huffman construction on sorted leaves.
	m := len(used)
	var weight [2 * nSym]uint64
	var parent [2 * nSym]int32
	for i, s := range used {
		weight[i] = uint64(freq[s])
	}
	leaf, node, next := 0, m, m
	pick := func() int {
		if leaf < m && (node >= next || weight[leaf] <= weight[node]) {
			leaf++
			return leaf - 1
		}
		node++
		return node - 1
	}
	for next < 2*m-1 {
		a, b := pick(), pick()
		weight[next] = weight[a] + weight[b]
		parent[a], parent[b] = int32(next), int32(next)
		next++
	}
	var count [64]int
	depth := make([]int, 2*m-1)
	maxDepth := 0
	for i := 2*m - 3; i >= 0; i-- {
		depth[i] = depth[parent[i]] + 1
		if i < m {
			count[depth[i]]++
			maxDepth = max(maxDepth, depth[i])
		}
	}
	// Limit lengths while keeping the code complete (JPEG Annex K.3).
	for i := maxDepth; i > maxLen; i-- {
		for count[i] > 0 {
			j := i - 2
			for count[j] == 0 {
				j--
			}
			count[i] -= 2
			count[i-1]++
			count[j+1] += 2
			count[j]--
		}
	}
	// Least frequent symbols get the longest codes.
	k := 0
	for l := min(maxDepth, maxLen); l > 0; l-- {
		for range count[l] {
			lens[used[k]] = byte(l)
			k++
		}
	}
}

// canonicalCodes assigns canonical codes: by length, then symbol order.
func canonicalCodes(lens []byte, codes []uint16) {
	var count [maxBits + 2]uint16
	for _, l := range lens {
		count[l]++
	}
	count[0] = 0
	var next [maxBits + 2]uint16
	code := uint16(0)
	for l := 1; l <= maxBits; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	for s, l := range lens {
		if l > 0 {
			codes[s] = next[l]
			next[l]++
		}
	}
}
