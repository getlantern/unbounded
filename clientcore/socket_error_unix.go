//go:build !wasm && !windows

package clientcore

import "syscall"

var socketErrnoClasses = map[syscall.Errno]string{
	syscall.ENETUNREACH:   "unreachable",
	syscall.EHOSTUNREACH:  "unreachable",
	syscall.EADDRNOTAVAIL: "addr_unavailable",
	syscall.EPERM:         "permission",
	syscall.EACCES:        "permission",
	syscall.ENOBUFS:       "no_buffers",
	syscall.EAFNOSUPPORT:  "family_unsupported",
}
