package main

import (
  "crypto/tls"
  "fmt"
  "log"
  "os"

  "github.com/getlantern/broflake/clientcore"
  "github.com/getlantern/broflake/common"
  "github.com/armon/go-socks5"
)

func main() {
  freddie := os.Getenv("FREDDIE")
  egress := os.Getenv("EGRESS")

  proxyPort := os.Getenv("PORT")
  if proxyPort == "" {
    proxyPort = "1080"
  }

  proxyIP := "127.0.0.1"

  tlsCertFile := os.Getenv("TLS_CERT_FILE")
  tlsKeyFile := os.Getenv("TLS_KEY_FILE")

  bfOpt := clientcore.NewDefaultBroflakeOptions()
  bfOpt.ClientType = "desktop"

  rtcOpt := clientcore.NewDefaultWebRTCOptions()
  rtcOpt.DiscoverySrv = freddie
  

  egOpt := clientcore.NewDefaultEgressOptions()
  egOpt.Addr = egress
  
  bfconn, _, err := clientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
  if err != nil {
    log.Fatal(err)
  }

  cert, err := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
  if err != nil {
    panic(err)
  }

  tlsConfig := &tls.Config{
    Certificates: []tls.Certificate{cert},
  }

  ql, err := clientcore.NewQUICLayer(bfconn, tlsConfig)
  if err != nil {
    common.Debugf("Cannot start local proxy: failed to create QUIC layer: %v", err)
    return
  }

  addr := fmt.Sprintf("%v:%v", proxyIP, proxyPort)

  go ql.ListenAndMaintainQUICConnection()
  conf := &socks5.Config{
    Dial: clientcore.CreateSOCKS5Dialer(ql),
  }
  socks5, err := socks5.New(conf)
  if err != nil {
    panic(err)
  }

  common.Debugf("Starting SOCKS5 proxy on %v...", addr)
  err = socks5.ListenAndServe("tcp", addr)
  if err != nil {
    panic(err)
  }
}