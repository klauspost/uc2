package ultra

import (
	"bytes"
	"fmt"
)

// Strict decodes src and verifies that it uses only the forms UC2's own
// encoder produces: complete codes, distances <= 64000 within the history,
// lengths 3..32760, EOB followed by L0, no empty blocks, default trees only
// for a final block of fewer than 256 words, and no data after the stream.
func Strict(src, dict []byte, size int64) error {
	br := bytes.NewReader(src)
	r := NewReader(br, dict, Unlimited)
	pos := int64(0)
	for block := 0; ; block++ {
		r.refill()
		if r.getBits(1) == 0 {
			break
		}
		r.refill()
		explicit := r.acc>>63 == 1
		if err := r.readTrees(); err != nil {
			return fmt.Errorf("block %d: %w", block, err)
		}
		if explicit {
			if r.overshoot {
				return fmt.Errorf("block %d: repeat runs past the tree", block)
			}
			if !complete(r.prevPre[:], preBits) || !complete(r.prev[:nLD], maxBits) || !complete(r.prev[nLD:], maxBits) {
				return fmt.Errorf("block %d: incomplete code", block)
			}
		}
		words := 0
		for {
			r.refill()
			s, err := r.sym(&r.ld)
			if err != nil {
				return err
			}
			if s < nLit {
				words++
				pos++
				continue
			}
			s -= nLit
			d := int(distBase[s])
			if x := uint(distExtra[s]); x > 0 {
				d += r.getBits(x)
			}
			r.refill()
			ls, err := r.sym(&r.l)
			if err != nil {
				return err
			}
			words += 2
			if d == eobDist {
				if ls != 0 {
					return fmt.Errorf("block %d: EOB with length symbol %d", block, ls)
				}
				break
			}
			n := int(lenBase[ls])
			if x := uint(lenExtra[ls]); x > 0 {
				n += r.getBits(x)
			}
			if d > MaxDist || int64(d) > int64(len(dict))+pos || n > MaxMatch {
				return fmt.Errorf("block %d: match len %d dist %d at %d", block, n, d, pos)
			}
			pos += int64(n)
		}
		if words == 2 {
			return fmt.Errorf("block %d: empty block", block)
		}
		if !explicit {
			r.refill()
			if r.acc>>63 != 0 || words >= 256 {
				return fmt.Errorf("block %d: default trees in a non-final or large block", block)
			}
		}
	}
	if pos != size {
		return fmt.Errorf("stream holds %d bytes, want %d", pos, size)
	}
	loaded := len(src) - (r.end - r.ip) - br.Len() + r.over
	consumed := loaded*8 - int(r.nbits)
	if len(src)%2 != 0 || (consumed+15)/16*2 != len(src) {
		return fmt.Errorf("stream is %d bytes, %d bits used", len(src), consumed)
	}
	if r.err != nil {
		return r.err
	}
	return nil
}

func complete(lens []byte, maxLen int) bool {
	k := 0
	for _, l := range lens {
		if l > 0 {
			k += 1 << (maxLen - int(l))
		}
	}
	return k == 1<<maxLen
}
