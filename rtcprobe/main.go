// rtcprobe measures plain pion WebRTC connections between a probe and a fixed responder, with
// none of Unbounded's signaling, matchmaking or volunteers in the way. It is a control for
// freddie.producer.outcomes: run the responder somewhere with an open NAT, run the probe from the
// network under test, and each attempt reports how long candidate gathering, ICE, DTLS and the
// data channel took, where it stalled if it did, and the throughput of a short transfer.
//
// The probe makes the offer and the responder answers, so the DTLS roles match production's: the
// offerer (Unbounded's consumer) is the DTLS server unless -responder-role says otherwise.
//
//	rtcprobe serve -listen :8080 -token SECRET -udp-ports 50000-50100
//	rtcprobe probe -url http://HOST:8080 -token SECRET -n 20 -dtls default
//	rtcprobe probe -url http://HOST:8080 -token SECRET -n 20 -dtls alt
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
