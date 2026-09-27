package uc2

import (
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/super"
	"github.com/klauspost/uc2/internal/ultra"
)

var (
	superDict = sync.OnceValue(func() *ultra.Dict { return ultra.NewDict([]byte(super.Data)) })
	zeroDict  = sync.OnceValue(func() *ultra.Dict { return ultra.NewDict(zero512) })
	encoders  = sync.Pool{New: func() any { return ultra.NewEncoder() }}
)

// job is one compressed piece (a stream or a fragment of one) or a raw copy.
// Jobs are written strictly in submission order.
type job struct {
	done   chan struct{}
	weight int64
	in     ultra.Input
	dict   func() *ultra.Dict
	delta  int
	out    ultra.Output
	sum    uint16 // checksum of the piece in the compressed domain
	src    io.ReaderAt
	srcOff int64
	srcLen int64
	rev    *wrev
	mas    *wmaster
	first  bool
	last   bool
}

func (j *job) run() {
	in := j.in
	if j.delta > 0 {
		buf := append(append(make([]byte, 0, len(in.Hist)+len(in.Data)), in.Hist...), in.Data...)
		d := ultra.NewDelta(j.delta)
		d.Encode(buf)
		in.Hist, in.Data = buf[:len(in.Hist)], buf[len(in.Hist):]
	}
	j.sum = format.Fletch(in.Data)
	if j.dict != nil {
		in.Dict = j.dict()
	}
	e := encoders.Get().(*ultra.Encoder)
	e.Encode(&in, &j.out)
	encoders.Put(e)
	j.in, j.dict = ultra.Input{}, nil
}

// streamState assembles fragments of the stream being written.
type streamState struct {
	bits ultra.Bits
	prev [344]byte
	off  int64
	sum  uint16
}

func (w *Writer) inflightMax() int64 {
	return 2*batchMax + int64(cap(w.tokens))*2*fragSize
}

func (w *Writer) submit(j *job) {
	if w.err != nil {
		return
	}
	j.done = make(chan struct{})
	for w.inflight > 0 && w.inflight+j.weight > w.inflightMax() {
		w.drain(true, false)
	}
	w.inflight += j.weight
	w.fifo = append(w.fifo, j)
	if j.src != nil {
		close(j.done)
	} else {
		w.tokens <- struct{}{}
		go func() {
			j.run()
			<-w.tokens
			close(j.done)
		}()
	}
	w.drain(false, false)
}

// drain writes finished jobs at the head of the queue. With block it waits
// for at least one job; with all it waits for every job.
func (w *Writer) drain(block, all bool) {
	for len(w.fifo) > 0 {
		j := w.fifo[0]
		if block || all {
			<-j.done
		} else {
			select {
			case <-j.done:
			default:
				return
			}
		}
		w.fifo[0] = nil
		w.fifo = w.fifo[1:]
		w.inflight -= j.weight
		w.output(j)
		block = false
	}
}

type outWriter struct{ w *Writer }

func (o outWriter) Write(p []byte) (int, error) {
	o.w.write(p)
	return len(p), o.w.err
}

func (w *Writer) output(j *job) {
	s := &w.stream
	if j.first {
		s.off, s.sum, s.prev = w.off, 0, ultra.BaseLens()
		s.bits.Reset()
	}
	if j.src != nil {
		if w.err == nil {
			n, err := io.Copy(outWriter{w}, io.NewSectionReader(j.src, j.srcOff, j.srcLen))
			if err == nil && n != j.srcLen {
				err = fmt.Errorf("%w: compressed data past the end of the source archive", ErrFormat)
			}
			w.fail(err)
		}
	} else {
		s.sum ^= j.sum ^ 0xA55A
		if j.first && j.last {
			w.write(ultra.End(&j.out.Bits))
		} else {
			ultra.Join(&s.bits, &s.prev, &j.out)
			if j.last {
				w.write(ultra.End(&s.bits))
			} else {
				w.write(s.bits.Drain())
			}
		}
		j.out = ultra.Output{}
	}
	if !j.last {
		return
	}
	off, n := s.off, w.off-s.off
	if j.mas != nil {
		j.mas.off, j.mas.comp = off, n
	}
	if j.rev != nil {
		j.rev.off, j.rev.comp = off, n
		if j.src == nil {
			j.rev.fletch = s.sum ^ 0xA55A
		}
	}
}
