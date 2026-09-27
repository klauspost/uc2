package uc2

import (
	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/tags"
)

func init() {
	tags.Record = func(f any) *format.Entry { return f.(*File).rec }
	tags.CDIR = func(r any) *format.CDIR { return r.(*Reader).cdir }
	tags.Source = func(w any) any {
		if a := w.(*Writer).app; a != nil {
			return a.r
		}
		return nil
	}
	tags.Replace = func(w, f any, t []format.Tag) error { return w.(*Writer).replaceTags(f.(*File), t) }
}
