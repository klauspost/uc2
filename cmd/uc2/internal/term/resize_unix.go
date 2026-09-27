//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package term

import (
	"os"
	"os/signal"
	"syscall"
)

// NotifyResize reports size changes of the terminal fd on the returned
// channel until stop is called.
func NotifyResize(fd int) (c <-chan struct{}, stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	return watch(func(done <-chan struct{}) bool {
		select {
		case <-sig:
			return true
		case <-done:
			return false
		}
	}, func() { signal.Stop(sig) })
}
