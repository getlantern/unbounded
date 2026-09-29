module unbounded.test/legacy-consumer

go 1.24.0

require github.com/quic-go/quic-go v0.59.0

require (
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
)

replace github.com/quic-go/quic-go => github.com/getlantern/quic-go-unbounded-fork v0.59.0-unbounded
