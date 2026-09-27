package ultra

import (
	"encoding/binary"
	"math/bits"
	"sync"
)

// Encoder parameters per UC2 method (ULTRACMP.CPP TuneComp): chain depth,
// lazy probe depth, length below which a lazy probe is made, and the length at
// which the search gives up looking for longer matches.
type level struct{ depth, lazyDepth, lazyLimit, giveUp int }

var levels = [6]level{
	1: {5, 2, 5, 25},
	2: {15, 2, 15, 25},
	3: {70, 10, 30, 50},
	4: {600, 50, 40, 100},
	5: {10000, 5000, 200, 100},
}

const (
	blockWords = 28 * 490 // UC2's Huffman buffer: literals take one word, matches two
	ringMask   = 1<<16 - 1
	// UC2 decodes blocks of any size; larger blocks only pay off on incompressible data.
	maxBlockWords = 4 * blockWords
)

// Input describes one piece of a stream to encode.
type Input struct {
	Dict    *Dict  // history preceding Data (masters); nil for none
	Hist    []byte // raw history preceding Data (later fragments); used when Dict is nil
	Data    []byte
	SeedOff int // Data[:SeedLen] equals Dict bytes at SeedOff and is emitted as matches
	SeedLen int
	Level   int
	First   bool // Data starts the stream
	Final   bool // the stream ends after Data
}

// Output is an encoded piece. Pieces are joined with Join.
type Output struct {
	Bits      Bits
	Deferred  bool       // the first block's tree header is written by Join
	FirstLens [nSym]byte // code lengths of the deferred first block
	LastLens  [nSym]byte // code lengths of the last block
}

// Encoder compresses data. It is not safe for concurrent use.
type Encoder struct {
	buf    []byte
	end    int
	head   [1 << hashBits]int32
	prev   [1 << 16]int32
	base   int32
	next   int32
	d      *Dict
	lv     level
	tokens []uint32
	ldFreq [nLD]uint32
	lFreq  [nL]uint32
	words  int
	prevL  [nSym]byte
	defer1 bool
	out    *Output
	final  bool
}

func NewEncoder() *Encoder { return &Encoder{next: 1} }

// Encode encodes in into out, replacing its previous content.
func (e *Encoder) Encode(in *Input, out *Output) {
	lv := in.Level
	if lv < 1 || lv > 5 {
		lv = 3
	}
	out.Bits.Reset()
	out.Deferred = false
	e.lv, e.out, e.final, e.d = levels[lv], out, in.Final, in.Dict
	e.tokens, e.words = e.tokens[:0], 0
	clear(e.ldFreq[:])
	clear(e.lFreq[:])
	e.prevL = baseLens
	e.defer1 = !in.First

	var hist []byte
	if in.Dict != nil {
		hist = in.Dict.data
	} else {
		hist = in.Hist[max(0, len(in.Hist)-MaxDist):]
	}
	start := len(hist)
	e.end = start + len(in.Data)
	e.buf = append(append(append(e.buf[:0], hist...), in.Data...), 0, 0, 0, 0, 0, 0, 0, 0)
	e.buf = e.buf[:e.end]
	if int64(e.next)+int64(len(e.buf)) > 1<<30 {
		clear(e.head[:])
		clear(e.prev[:])
		e.next = 1
	}
	e.base = e.next
	e.next += int32(len(e.buf)) + 1

	if in.Dict == nil {
		for i := range start {
			e.insert(i)
		}
	} else {
		// The dictionary index lacks positions whose bytes reach into Data.
		for i := max(0, start-2); i < start; i++ {
			e.insert(i)
		}
	}
	pos := start
	if in.Dict != nil && in.SeedLen > 0 {
		dist := start - in.SeedOff
		for left := in.SeedLen; left > 0; {
			n := min(left, MaxMatch)
			if left-n < MinMatch && left-n > 0 {
				n = left - MinMatch
			}
			if n < MinMatch {
				break
			}
			e.match(n, dist)
			left -= n
			pos += n
		}
	}
	e.parse(pos)
	if len(e.tokens) > 0 {
		e.flush()
	}
	out.LastLens = e.prevL
}

func (e *Encoder) insert(p int) {
	if p+3 > e.end {
		return
	}
	h := hash3(e.buf[:cap(e.buf)], p)
	a := e.base + int32(p)
	e.prev[a&ringMask] = e.head[h]
	e.head[h] = a
}

func matchLen(b []byte, a, p, limit int) int {
	n := 0
	for n+8 <= limit {
		if x := binary.LittleEndian.Uint64(b[a+n:]) ^ binary.LittleEndian.Uint64(b[p+n:]); x != 0 {
			return n + bits.TrailingZeros64(x)>>3
		}
		n += 8
	}
	for n < limit && b[a+n] == b[p+n] {
		n++
	}
	return n
}

// find returns the longest match at pos that is longer than minLen, searching
// at most depth candidates, newest first. It inserts pos.
func (e *Encoder) find(pos, depth, minLen int) (best, bestPos int) {
	b := e.buf[:cap(e.buf)]
	limit := min(MaxMatch, e.end-pos)
	best = minLen
	if best >= limit {
		e.insert(pos)
		return 0, 0
	}
	h := hash3(b, pos)
	giveUp := e.lv.giveUp
	check := func(p int) bool {
		if b[p+best] == b[pos+best] {
			if l := matchLen(b, p, pos, limit); l > best {
				best, bestPos = l, p
				return l > giveUp || l == limit
			}
		}
		return false
	}
	n := depth
	c := e.head[h]
	for n > 0 && c >= e.base {
		p := int(c - e.base)
		if pos-p > MaxDist {
			n = -1
			break
		}
		n--
		if check(p) {
			goto done
		}
		c = e.prev[c&ringMask]
	}
	if e.d != nil && n > 0 {
		for p := int(e.d.head[h]) - 1; p >= 0 && n > 0 && pos-p <= MaxDist; n-- {
			if check(p) {
				break
			}
			d := e.d.prev[p]
			if d == 0 {
				break
			}
			p -= int(d)
		}
	}
done:
	e.insert(pos)
	if best == minLen {
		return 0, 0
	}
	return best, bestPos
}

const maxDist3 = 1535

// parse follows UC2's greedy parse with one lazy probe (ULTRACMP.CPP).
func (e *Encoder) parse(pos int) {
	lv, end, b := e.lv, e.end, e.buf
	var lazy bool
	var tl, tp, misses int
	for pos < end {
		var l, p int
		switch {
		case lazy:
			l, p, lazy = tl, tp, false
		case end-pos >= MinMatch:
			l, p = e.find(pos, lv.depth, MinMatch-1)
			// Far 3-byte matches cost more than three literals.
			if l == MinMatch && pos-p > maxDist3 {
				l = 0
			}
		}
		if l < MinMatch {
			e.lit(b[pos])
			pos++
			// On incompressible data, search progressively less often.
			if misses++; misses > 256 {
				for n := min(misses>>8, 16, end-pos); n > 0; n-- {
					e.lit(b[pos])
					pos++
				}
			}
			continue
		}
		misses = 0
		if l < lv.lazyLimit && end-(pos+1) >= MinMatch {
			tl, tp = e.find(pos+1, lv.lazyDepth, l)
			if tl > l {
				e.lit(b[pos])
				pos++
				lazy = true
				continue
			}
		} else {
			e.insert(pos + 1)
		}
		for i := pos + 2; i < pos+l; i++ {
			e.insert(i)
		}
		e.match(l, pos-p)
		pos += l
	}
}

func distSym(d int) int {
	switch {
	case d < 16:
		return 255 + d
	case d < 256:
		return 270 + d>>4
	case d < 4096:
		return 285 + d>>8
	}
	return 300 + d>>12
}

func lenSym(l int) int {
	switch {
	case l < 11:
		return l - 3
	case l < 27:
		return 8 + (l-11)>>1
	case l < 91:
		return 16 + (l-27)>>3
	case l < 155:
		return 24
	case l < 667:
		return 25
	case l < 2715:
		return 26
	}
	return 27
}

// full reports whether the block should end. Blocks with few matches grow,
// since their tree headers cost more than adapting to changing statistics gains.
func (e *Encoder) full() bool {
	matches := e.words - len(e.tokens)
	return e.words >= blockWords && (e.words >= maxBlockWords || matches*64 >= e.words)
}

func (e *Encoder) lit(c byte) {
	e.tokens = append(e.tokens, uint32(c))
	e.ldFreq[c]++
	if e.words++; e.full() {
		e.flush()
	}
}

func (e *Encoder) match(l, d int) {
	e.tokens = append(e.tokens, uint32(l)<<16|uint32(d))
	e.ldFreq[distSym(d)]++
	e.lFreq[lenSym(l)]++
	if e.words += 2; e.full() {
		e.flush()
	}
}

// flush writes the buffered tokens as one block, terminated by EOB.
func (e *Encoder) flush() {
	e.ldFreq[nLD-1]++
	e.lFreq[0]++
	var lens [nSym]byte
	huffLengths(e.ldFreq[:], maxBits, lens[:nLD])
	huffLengths(e.lFreq[:], maxBits, lens[nLD:])
	b := &e.out.Bits
	switch {
	case e.defer1:
		e.out.Deferred, e.out.FirstLens, e.defer1 = true, lens, false
	case e.final && e.words+2 < 256 && e.defaultCheaper(&lens):
		b.Put(1, 1)
		b.Put(0, 1)
		lens = baseLens
	default:
		b.Put(1, 1)
		writeTrees(b, &lens, &e.prevL)
	}
	e.prevL = lens
	var codes [nSym]uint16
	canonicalCodes(lens[:nLD], codes[:nLD])
	canonicalCodes(lens[nLD:], codes[nLD:])
	for _, t := range e.tokens {
		l := int(t >> 16)
		if l == 0 {
			b.Put(uint32(codes[t]), uint(lens[t]))
			continue
		}
		d := int(t & 0xFFFF)
		s := distSym(d)
		b.Put(uint32(codes[s]), uint(lens[s]))
		if x := distExtra[s-nLit]; x > 0 {
			b.Put(uint32(d-int(distBase[s-nLit])), uint(x))
		}
		ls := lenSym(l)
		b.Put(uint32(codes[nLD+ls]), uint(lens[nLD+ls]))
		if x := lenExtra[ls]; x > 0 {
			b.Put(uint32(l-int(lenBase[ls])), uint(x))
		}
	}
	b.Put(uint32(codes[nLD-1]), uint(lens[nLD-1]))
	b.Put(eobDist-uint32(distBase[nLD-1-nLit]), 12)
	b.Put(uint32(codes[nLD]), uint(lens[nLD]))
	e.tokens, e.words = e.tokens[:0], 0
	clear(e.ldFreq[:])
	clear(e.lFreq[:])
}

// defaultCheaper reports whether coding the block with the default trees is
// smaller than with lens plus their tree header.
func (e *Encoder) defaultCheaper(lens *[nSym]byte) bool {
	var hdr Bits
	writeTrees(&hdr, lens, &e.prevL)
	own, def := hdr.Len()-1, 0
	for s, f := range e.ldFreq {
		own += int(f) * int(lens[s])
		def += int(f) * int(baseLens[s])
	}
	for s, f := range e.lFreq {
		own += int(f) * int(lens[nLD+s])
		def += int(f) * int(baseLens[nLD+s])
	}
	return def <= own
}

// writeTrees writes an explicit tree header (TREEENC.CPP TreeEnc): lengths
// delta coded against prev, run length coded, and Huffman coded with a pre-tree.
func writeTrees(b *Bits, lens, prev *[nSym]byte) {
	b.Put(1, 1)
	t := uint32(0)
	for i := range 32 {
		if i != 9 && i != 10 && i != 12 && i != 13 && lens[i] != 0 {
			t |= 1
		}
	}
	for i := 128; i < 256; i++ {
		if lens[i] != 0 {
			t |= 2
		}
	}
	var st [nSym]byte
	n := 0
	add := func(lo, hi int) {
		for s := lo; s < hi; s++ {
			st[n] = deltaEnc[prev[s]][lens[s]]
			n++
		}
	}
	if t&1 != 0 {
		add(0, 32)
	} else {
		add(9, 11)
		add(12, 14)
	}
	add(32, 128)
	if t&2 != 0 {
		add(128, 256)
	}
	add(256, nSym)

	var rle [nSym + 2]byte
	var freq [nPre]uint32
	m := 0
	for i := 0; i < n; {
		j := i + 1
		for j < n && st[j] == st[i] {
			j++
		}
		run := j - i
		if run > minRepeat {
			run = min(run, repeatSym+minRepeat)
			rle[m], rle[m+1], rle[m+2] = st[i], repeatSym, byte(run-minRepeat)
			freq[st[i]]++
			freq[repeatSym]++
			freq[run-minRepeat]++
			m += 3
		} else {
			rle[m] = st[i]
			freq[st[i]]++
			m++
			run = 1
		}
		i += run
	}
	var pl [nPre]byte
	var pc [nPre]uint16
	huffLengths(freq[:], preBits, pl[:])
	canonicalCodes(pl[:], pc[:])
	b.Put(t, 2)
	for _, l := range pl {
		b.Put(uint32(l), 3)
	}
	for _, v := range rle[:m] {
		b.Put(uint32(pc[v]), uint(pl[v]))
	}
}

// Join appends piece o to stream s; prev holds the tree lengths of the stream's last block.
func Join(s *Bits, prev *[nSym]byte, o *Output) {
	if o.Deferred {
		s.Put(1, 1)
		writeTrees(s, &o.FirstLens, prev)
	}
	s.Append(&o.Bits)
	*prev = o.LastLens
}

// End terminates stream s and returns its bytes.
func End(s *Bits) []byte {
	s.Put(0, 1)
	return s.Finish()
}

// BaseLens returns the tree lengths at the start of a stream, for Join.
func BaseLens() [nSym]byte { return baseLens }

var encoders = sync.Pool{New: func() any { return NewEncoder() }}

// Compress encodes data as a complete stream with dict as history.
func Compress(data []byte, dict *Dict, level int) []byte {
	e := encoders.Get().(*Encoder)
	defer encoders.Put(e)
	var out Output
	e.Encode(&Input{Dict: dict, Data: data, Level: level, First: true, Final: true}, &out)
	return End(&out.Bits)
}
