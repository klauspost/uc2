//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows

package term

import "sync"

// watch sends a notification each time changed reports a change, until
// changed returns false after done is closed by stop.
func watch(changed func(done <-chan struct{}) bool, stop func()) (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		for changed(done) {
			select {
			case c <- struct{}{}:
			default: // one pending notification is enough
			}
		}
	}()
	return c, sync.OnceFunc(func() {
		stop()
		close(done)
	})
}
