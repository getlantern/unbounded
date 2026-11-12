//go:build !wasm

package clientcore

import (
	"errors"
	"syscall"
)

// socketErrorClass is the class, from common's preflight causes, of an error opening or sending
// on a UDP socket. Only the class leaves the producer, never the error.
func socketErrorClass(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if class, ok := socketErrnoClasses[errno]; ok {
			return class
		}
	}
	return "other"
}
