package term

import "time"

// NotifyResize reports size changes of the console output fd on the
// returned channel until stop is called. It polls: consoles send no
// signal, and their resize events would need ReadConsoleInput instead of
// the VT input of raw mode.
func NotifyResize(fd int) (c <-chan struct{}, stop func()) {
	t := time.NewTicker(250 * time.Millisecond)
	w, h, _ := getSize(fd)
	return watch(func(done <-chan struct{}) bool {
		for {
			select {
			case <-t.C:
				if nw, nh, err := getSize(fd); err == nil && (nw != w || nh != h) {
					w, h = nw, nh
					return true
				}
			case <-done:
				return false
			}
		}
	}, t.Stop)
}
