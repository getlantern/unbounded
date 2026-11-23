// rtcprobe measures plain pion WebRTC connections between a probe and a fixed responder, with
// none of Unbounded's signaling, matchmaking or volunteers in the way. It is a control for
// freddie.producer.outcomes: run the responder somewhere with an open NAT, run the probe from the
// network under test, and each attempt reports how long candidate gathering, ICE, DTLS and the
// data channel took, where it stalled if it did, and the throughput of a short transfer.
//
// The probe makes the offer and the responder answers, so the DTLS roles match production's: the
// offerer (Unbounded's consumer) is the DTLS server unless -responder-role says otherwise.
//
//	rtcprobe serve -listen :8443 -token SECRET -tls -udp-ports 50000-50100   # logs the certificate pin
//	rtcprobe probe -url https://HOST:8443 -token SECRET -pin PIN -n 20
//	rtcprobe probe ... -responder-role server -dtls default   # the probe sends pion's ClientHello
//	rtcprobe probe ... -responder-role server -dtls covert    # ... or covert-dtls's
//
// With the default -responder-role client, the probe is the DTLS server and sends pion's
// ServerHello, as a production consumer does to a browser volunteer.
//
// The probe prints one JSON line per attempt on stdout and a summary on stderr.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "probe":
		err = probe(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rtcprobe:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rtcprobe serve|probe [flags]  (rtcprobe serve -h, rtcprobe probe -h)")
	os.Exit(2)
}
