//go:build windows

package clientcore

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock reports its own codes, not the POSIX ones.
var socketErrnoClasses = map[syscall.Errno]string{
	windows.WSAENETUNREACH:   "unreachable",
	windows.WSAEHOSTUNREACH:  "unreachable",
	windows.WSAEADDRNOTAVAIL: "addr_unavailable",
	windows.WSAEACCES:        "permission",
	windows.WSAENOBUFS:       "no_buffers",
	windows.WSAEAFNOSUPPORT:  "family_unsupported",
}
