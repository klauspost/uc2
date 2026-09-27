//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package term

// NotifyResize reports nothing where terminals are not supported.
func NotifyResize(fd int) (c <-chan struct{}, stop func()) { return nil, func() {} }
