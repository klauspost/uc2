// Package ultra implements UltraCompressor II's "Ultra" LZ77+Huffman bitstream.
//
// A stream is a sequence of blocks, each starting with a 1 bit followed by the
// Huffman trees, and is terminated by a 0 bit. Bits are read MSB first from
// little-endian 16-bit words. The history window is a dictionary ("master")
// followed by the output.
package ultra

import "errors"

var ErrCorrupt = errors.New("uc2: corrupt compressed data")

const (
	nLit      = 256
	nLD       = 316 // literals + 60 distance slots
	nL        = 28  // length symbols
	nSym      = nLD + nL
	nPre      = 15 // pre-tree: 14 delta codes + repeat marker
	repeatSym = 14
	minRepeat = 6
	maxBits   = 13
	preBits   = 7

	MaxDist     = 64000 // largest distance UC2's encoder emits
	eobDist     = 64001
	MinMatch    = 3
	MaxMatch    = 32760 // largest length UC2's encoder emits
	maxDecLen   = 35482 // largest length the format can express
	HistorySize = 65536
)

// Lengths of symbols in the default ("base") trees (TREEENC.CPP BasePrev).
var baseLens = func() (b [nSym]byte) {
	fill := func(lo, hi int, v byte) {
		for i := lo; i < hi; i++ {
			b[i] = v
		}
	}
	fill(0, 32, 9)
	b[10], b[12], b[32] = 7, 7, 7
	fill(33, 128, 8)
	b[46], b[58], b[92] = 7, 7, 7
	fill(128, 256, 10)
	fill(256, 272, 6)
	fill(272, 284, 7)
	fill(284, 290, 8)
	fill(290, 300, 9)
	fill(300, 316, 10)
	fill(316, 325, 4)
	fill(325, 334, 5)
	fill(334, 344, 6)
	return b
}()

// deltaEnc[prev][new] and deltaDec[prev][code] translate code lengths to and
// from delta codes relative to the previous tree (TREEENC.CPP InitTables).
var deltaEnc, deltaDec = func() (table, vval [14][14]byte) {
	const n = 14
	for i := 1; i < n; i++ {
		vval[0][i], table[0][i] = byte(n-i), byte(n-i)
	}
	for i := 1; i < n; i++ {
		prob := 0
		x := n - i + 1
		if i < 9 {
			x = i - 1
		}
		j := 1
		for ; j < x; j++ {
			table[i][i+j-1] = byte(prob)
			vval[i][prob] = byte(i + j - 1)
			prob++
			table[i][i-j] = byte(prob)
			vval[i][prob] = byte(i - j)
			prob++
		}
		table[i][i-j] = byte(prob + 1)
		vval[i][prob+1] = byte(i - j)
		if i < 8 {
			table[i][0] = n - 1
			vval[i][n-1] = 0
			table[i][i+j-1] = byte(prob)
			vval[i][prob] = byte(i + j - 1)
			for j = n - 1; j >= i*2-1; j-- {
				table[i][j] = byte(j - 1)
				vval[i][j-1] = byte(j)
			}
		} else {
			table[i][0] = byte(prob)
			vval[i][prob] = 0
			for j = 1; j <= i*2-15; j++ {
				table[i][j] = byte(n - j)
				vval[i][n-j] = byte(j)
			}
		}
	}
	return
}()

// Distance slots: symbol 256+i covers distances distBase[i] with distExtra[i] extra bits.
var distBase, distExtra = func() (base [60]uint16, extra [60]uint8) {
	for i := range 15 {
		base[i] = uint16(i + 1)
		base[15+i], extra[15+i] = uint16(i+1)<<4, 4
		base[30+i], extra[30+i] = uint16(i+1)<<8, 8
		base[45+i], extra[45+i] = uint16(i+1)<<12, 12
	}
	return
}()

var lenBase = [nL]uint16{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 21, 23, 25, 27, 35, 43, 51, 59, 67, 75, 83, 91, 155, 667, 2715}
var lenExtra = [nL]uint8{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 3, 3, 3, 3, 3, 3, 3, 3, 6, 9, 11, 15}
