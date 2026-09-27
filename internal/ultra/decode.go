package ultra

import (
	"encoding/binary"
	"io"
	"math"
	"sync"
)

const (
	inBufSize = 64 << 10
	outChunk  = 128 << 10
	winLimit  = HistorySize + outChunk
	winSize   = winLimit + maxDecLen
	stHeader  = 0
	stBlock   = 1
	stDone    = 2
	Unlimited = math.MaxInt64
)

// Reader decodes an Ultra stream. The dictionary precedes the output in the
// history; decoding stops once size bytes have been produced.
type Reader struct {
	src    io.Reader
	buf    []byte
	ip     int
	end    int
	srcEOF bool
	over   int // zero bytes supplied past the end of the input

	acc   uint64 // left-aligned bit accumulator
	nbits uint

	prev      [nSym]byte
	prevPre   [nPre]byte // pre-tree lengths of the last explicit tree header
	overshoot bool       // the last tree header's repeat ran past its end
	ld, l     decTable
	pre       decTable
	state     int
	left      int64
	sized     bool
	win       []byte
	wpos      int
	rpos      int
	err       error
}

// NewReader returns a Reader decoding src with dict as history. Use size Unlimited
// to decode until the end-of-stream bit.
func NewReader(src io.Reader, dict []byte, size int64) *Reader {
	r := &Reader{buf: make([]byte, inBufSize), win: make([]byte, winSize)}
	r.Reset(src, dict, size)
	return r
}

// Reset prepares r for a new stream. len(dict) must be at most HistorySize.
func (r *Reader) Reset(src io.Reader, dict []byte, size int64) {
	*r = Reader{src: src, buf: r.buf, win: r.win, left: size, sized: size != Unlimited, prev: baseLens}
	r.wpos = copy(r.win, dict)
	r.rpos = r.wpos
	if size == 0 {
		r.state = stDone
	}
}

func (r *Reader) Read(p []byte) (int, error) {
	for r.rpos == r.wpos {
		if r.err != nil {
			return 0, r.err
		}
		if r.state == stDone {
			if r.sized && r.left > 0 {
				r.err = io.ErrUnexpectedEOF
				continue
			}
			return 0, io.EOF
		}
		if r.wpos >= winLimit {
			r.wpos = copy(r.win, r.win[r.wpos-HistorySize:r.wpos])
			r.rpos = r.wpos
		}
		// Source errors set during refills must survive an end of stream.
		if err := r.decode(); r.err == nil {
			r.err = err
		}
	}
	n := copy(p, r.win[r.rpos:r.wpos])
	r.rpos += n
	return n, nil
}

// WriteTo decodes the remainder of the stream to w.
func (r *Reader) WriteTo(w io.Writer) (int64, error) {
	var total int64
	for {
		if r.rpos < r.wpos {
			n, err := w.Write(r.win[r.rpos:r.wpos])
			total += int64(n)
			r.rpos += n
			if err != nil {
				return total, err
			}
		}
		if _, err := r.Read(nil); err != nil {
			if err == io.EOF {
				err = nil
			}
			return total, err
		}
	}
}

func (r *Reader) fillInput() {
	if r.srcEOF {
		return
	}
	r.end = copy(r.buf, r.buf[r.ip:r.end])
	r.ip = 0
	for empty := 0; r.end < len(r.buf) && !r.srcEOF; {
		n, err := r.src.Read(r.buf[r.end:])
		r.end += n
		if n == 0 && err == nil {
			if empty++; empty >= 100 {
				err = io.ErrNoProgress
			}
		}
		if err != nil {
			r.srcEOF = true
			if err != io.EOF {
				r.err = err
			}
		}
		if r.end >= 8 {
			break
		}
	}
}

// refill tops the accumulator up to at least 49 bits. Past the end of the input
// zero bits are supplied, as UC2 does; consuming more than a word of them is an error.
func (r *Reader) refill() {
	if r.end-r.ip < 8 {
		r.fillInput()
		if r.end-r.ip < 8 {
			r.refillSlow()
			return
		}
	}
	v := binary.LittleEndian.Uint64(r.buf[r.ip:])
	v = v<<32 | v>>32
	v = (v&0x0000FFFF0000FFFF)<<16 | (v>>16)&0x0000FFFF0000FFFF
	n := (64 - r.nbits) &^ 15
	r.acc |= v >> (64 - n) << (64 - n - r.nbits)
	r.nbits += n
	r.ip += int(n >> 3)
}

func (r *Reader) refillSlow() {
	if r.over*8 > int(r.nbits)+16 && r.err == nil {
		r.err = io.ErrUnexpectedEOF
	}
	for r.nbits <= 48 {
		var w uint64
		switch r.end - r.ip {
		case 0:
			r.over += 2
		case 1:
			w = uint64(r.buf[r.ip])
			r.ip++
			r.over++
		default:
			w = uint64(binary.LittleEndian.Uint16(r.buf[r.ip:]))
			r.ip += 2
		}
		r.acc |= w << (48 - r.nbits)
		r.nbits += 16
	}
}

func (r *Reader) getBits(n uint) int {
	v := int(r.acc >> (64 - n))
	r.acc <<= n
	r.nbits -= n
	return v
}

func (r *Reader) sym(t *decTable) (int, error) {
	e := t.t[r.acc>>(64-t.bits)]
	n := uint(e >> 9)
	if n == 0 {
		return 0, ErrCorrupt
	}
	r.acc <<= n
	r.nbits -= n
	return int(e & 511), nil
}

func (r *Reader) decode() error {
	for r.wpos < winLimit && r.err == nil {
		switch r.state {
		case stDone:
			return nil
		case stHeader:
			r.refill()
			if r.getBits(1) == 0 {
				r.state = stDone
				return nil
			}
			if err := r.readTrees(); err != nil {
				return err
			}
			r.state = stBlock
		case stBlock:
			if err := r.block(); err != nil {
				return err
			}
		}
	}
	return r.err
}

func (r *Reader) readTrees() error {
	if r.getBits(1) == 1 {
		t := r.getBits(2)
		total := nSym
		if t&1 == 0 {
			total -= 28
		}
		if t&2 == 0 {
			total -= 128
		}
		var pl [nPre]byte
		for i := range pl {
			r.refill()
			pl[i] = byte(r.getBits(3))
		}
		r.prevPre = pl
		if err := r.pre.build(pl[:]); err != nil {
			return err
		}
		var st [nSym + 20]byte
		cnt, val := 0, byte(0)
		for cnt < total {
			r.refill()
			s, err := r.sym(&r.pre)
			if err != nil {
				return err
			}
			if s != repeatSym {
				val = byte(s)
				st[cnt] = val
				cnt++
				continue
			}
			c, err := r.sym(&r.pre)
			if err != nil {
				return err
			}
			for range c + minRepeat - 1 {
				st[cnt] = val
				cnt++
			}
		}
		r.overshoot = cnt != total
		var lens [nSym]byte
		i := 0
		put := func(lo, hi int) {
			for s := lo; s < hi; s++ {
				lens[s] = deltaDec[r.prev[s]][st[i]]
				i++
			}
		}
		if t&1 != 0 {
			put(0, 32)
		} else {
			put(9, 11)
			put(12, 14)
		}
		put(32, 128)
		if t&2 != 0 {
			put(128, 256)
		}
		put(256, nSym)
		r.prev = lens
	} else {
		r.prev = baseLens
		r.ld, r.l = baseTables()
		return nil
	}
	if err := r.ld.build(r.prev[:nLD]); err != nil {
		return err
	}
	return r.l.build(r.prev[nLD:])
}

// baseTables returns the decode tables of the default trees.
var baseTables = sync.OnceValues(func() (ld, l decTable) {
	ld.build(baseLens[:nLD])
	l.build(baseLens[nLD:])
	return
})

// block decodes symbols until the window is full, the block ends or the size is reached.
// The bit reader state is kept in locals; refills need up to 25 bits for a
// literal/distance code with its extra bits and 28 for a length code.
func (r *Reader) block() error {
	win, wpos, left := r.win, r.wpos, r.left
	acc, nbits := r.acc, r.nbits
	ldt, lt := &r.ld.t, &r.l.t
	ldShift, lShift := 64-uint(r.ld.bits), 64-uint(r.l.bits)
	var err error
	for wpos < winLimit {
		if left == 0 {
			r.state = stDone
			break
		}
		if nbits < 32 {
			if r.end-r.ip >= 8 {
				v := binary.LittleEndian.Uint64(r.buf[r.ip:])
				v = v<<32 | v>>32
				v = (v&0x0000FFFF0000FFFF)<<16 | (v>>16)&0x0000FFFF0000FFFF
				n := (64 - nbits) &^ 15
				acc |= v >> (64 - n) << (64 - n - nbits)
				nbits += n
				r.ip += int(n >> 3)
			} else {
				r.acc, r.nbits = acc, nbits
				r.refill()
				acc, nbits = r.acc, r.nbits
			}
		}
		e := ldt[acc>>ldShift]
		n := uint(e >> 9)
		if n == 0 {
			err = ErrCorrupt
			break
		}
		acc <<= n
		nbits -= n
		sym := int(e & 511)
		if sym < nLit {
			win[wpos] = byte(sym)
			wpos++
			left--
			continue
		}
		sym -= nLit
		x := uint(distExtra[sym])
		dist := int(distBase[sym]) + int(acc>>(64-x))
		acc <<= x
		nbits -= x
		if nbits < 28 {
			r.acc, r.nbits = acc, nbits
			r.refill()
			acc, nbits = r.acc, r.nbits
		}
		e = lt[acc>>lShift]
		n = uint(e >> 9)
		if n == 0 {
			err = ErrCorrupt
			break
		}
		acc <<= n
		nbits -= n
		if dist == eobDist {
			r.state = stHeader
			break
		}
		ls := int(e & 511)
		x = uint(lenExtra[ls])
		length := int(lenBase[ls]) + int(acc>>(64-x))
		acc <<= x
		nbits -= x
		if dist > wpos {
			err = ErrCorrupt
			break
		}
		if int64(length) > left {
			length = int(left)
		}
		src := wpos - dist
		if dist >= length {
			copy(win[wpos:wpos+length], win[src:src+length])
			wpos += length
		} else {
			end := wpos + length
			for wpos < end {
				wpos += copy(win[wpos:end], win[src:wpos])
			}
		}
		left -= int64(length)
	}
	r.wpos, r.left, r.acc, r.nbits = wpos, left, acc, nbits
	return err
}
