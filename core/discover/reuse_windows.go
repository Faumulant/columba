//go:build windows

package discover

import "syscall"

// setReuseAddr 允许同机多个 Drop 进程共用组播发现端口。
// 仅组播端口，无单播端口被劫持的风险面。
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return serr
}
