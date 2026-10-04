//go:build linux

package localipc

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func peerUserID(connection net.Conn) (uint32, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, errors.New("localipc: accepted connection is not Unix")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("localipc: access peer socket: %w", err)
	}
	var (
		uid        uint32
		controlErr error
	)
	if err := raw.Control(func(fd uintptr) {
		credential, err := unix.GetsockoptUcred(
			int(fd),
			unix.SOL_SOCKET,
			unix.SO_PEERCRED,
		)
		if err != nil {
			controlErr = err
			return
		}
		uid = credential.Uid
	}); err != nil {
		return 0, fmt.Errorf("localipc: inspect peer socket: %w", err)
	}
	if controlErr != nil {
		return 0, fmt.Errorf("localipc: read peer credential: %w", controlErr)
	}
	return uid, nil
}
