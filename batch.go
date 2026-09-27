package uc2

import (
	"math"
	"sync"

	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/ultra"
)

// flushBatch compresses the batched small entries. Following UC2, files are
// grouped by type (format.ToKey) and each group gets a master built from the
// leading bytes of its files; files wholly inside their master cost a few bytes.
func (w *Writer) flushBatch() {
	entries := w.batch
	w.batch, w.batchN = nil, 0
	if len(entries) == 0 || w.err != nil {
		return
	}
	level := w.cfg.level
	if level >= Tight {
		var wg sync.WaitGroup
		for _, e := range entries {
			if len(e.buf) > 1500 {
				w.tokens <- struct{}{}
				wg.Go(func() {
					e.delta = ultra.Analyze(e.buf, int(level))
					<-w.tokens
				})
			}
		}
		wg.Wait()
	}

	type group struct {
		key   uint32
		files []*entryWriter
	}
	var groups []*group
	byKey := map[uint32]*group{}
	for _, e := range entries {
		if e.delta > 0 || len(e.buf) == 0 {
			continue
		}
		k := format.ToKey(e.name)
		g := byKey[k]
		if g == nil {
			g = &group{key: k}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.files = append(g.files, e)
	}
	quota := format.MaxMaster
	if level == Fast {
		quota = 32768
	}
	var masters []*wmaster
	for _, g := range groups {
		var content []byte
		for _, class := range [3][2]int{{2000, 10000}, {0, 2000}, {10000, math.MaxInt}} {
			for _, e := range g.files {
				n := min(quota-len(content), len(e.buf))
				if len(e.buf) < class[0] || len(e.buf) >= class[1] || n <= 0 {
					continue
				}
				e.seedOff, e.seedLen = len(content), n
				content = append(content, e.buf[:n]...)
			}
		}
		content = append(content, make([]byte, max(512-len(content), (512-len(content)%512)%512))...)
		m := w.newMaster(g.key, content)
		m.dict = sync.OnceValue(func() *ultra.Dict { return ultra.NewDict(content) })
		for _, e := range g.files {
			e.rev.master = m
		}
		masters = append(masters, m)
	}
	// Group by group, so a group's dictionary is freed once its files are done.
	submit := func(e *entryWriter, extra int64) {
		e.rev.method = deltaMethod(level, e.delta)
		j := &job{rev: e.rev, delta: e.delta, first: true, last: true, dict: e.rev.master.dict, weight: 2*int64(len(e.buf)) + 64<<10 + extra}
		j.in = ultra.Input{Data: e.buf, Level: int(level), First: true, Final: true, SeedOff: e.seedOff, SeedLen: e.seedLen}
		e.buf = nil
		w.submit(j)
	}
	for i, g := range groups {
		m := masters[i]
		for k, e := range g.files {
			extra := int64(0)
			if k == len(g.files)-1 {
				extra = 1<<17 + 3*int64(m.length)
			}
			submit(e, extra)
		}
		w.submitMaster(m)
		m.dict = nil
	}
	for _, e := range entries {
		if e.rev.master == nil {
			e.rev.master = w.zeroMaster()
			submit(e, 0)
		}
	}
}

func (w *Writer) newMaster(key uint32, content []byte) *wmaster {
	m := &wmaster{index: w.nextMaster, key: key, length: len(content), method: uint16(w.cfg.level), prefix: format.SuperMaster, content: content}
	w.nextMaster++
	w.masters = append(w.masters, m)
	return m
}

func (w *Writer) submitMaster(m *wmaster) {
	j := &job{mas: m, first: true, last: true, dict: superDict, weight: 2 * int64(len(m.content))}
	j.in = ultra.Input{Data: m.content, Level: int(w.cfg.level), First: true, Final: true}
	m.content = nil
	w.submit(j)
}

// zeroMaster returns the archive's 512-zero master, used by files that gain
// nothing from a dictionary (UC2 itself only uses custom masters for files).
func (w *Writer) zeroMaster() *wmaster {
	if w.zero == nil {
		w.zero = w.newMaster(0, zero512)
		w.zero.dict = zeroDict
		w.submitMaster(w.zero)
	}
	return w.zero
}
