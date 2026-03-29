# Unbounded: private swarm tutorial
In this tutorial, we'll show you how to transform your friends into a private volunteer 
circumvention network for a loved one living in a censored part of the world. No tech skills 
required for volunteers &mdash; if your friends know how to open a web browser, they can help!

### Components of the system
We'll need to create 4 components:

* Egress server (exit node for proxied requests)
* Signaling server (powers swarm discovery)
* Censored client (distribute this to your loved one living under internet censorship)
* Volunteer browser client (a website your volunteers will visit to join the swarm)

### Egress server

:arrow_right: [https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/egress-server](egress-server)

Unbounded supports two proxy modes: HTTP or SOCKS5. An egress server can support one mode or the other,
but not both. Here we create a SOCKS5 egress server. (See [https://github.com/getlantern/unbounded/blob/main/egress/cmd/http](here) for an HTTP mode example.)

Deploy this to your favorite cloud hosting provider:

```
package main

import (
  "context"
  "crypto/tls"
  "fmt"
  "net"
  "os"

  "github.com/armon/go-socks5"

  "github.com/getlantern/broflake/common"
  "github.com/getlantern/broflake/egress"
)

func main() {
  ctx := context.Background()
  port := os.Getenv("PORT")
  if port == "" {
    port = "8000"
  }

  tlsCertFile := os.Getenv("TLS_CERT_FILE")
  tlsKeyFile := os.Getenv("TLS_KEY_FILE")

  cert, err := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
  if err != nil {
    panic(err)
  }

  tlsConfig := &tls.Config{
    Certificates: []tls.Certificate{cert},
  }

  l, err := net.Listen("tcp", fmt.Sprintf(":%v", port))
  if err != nil {
    panic(err)
  }


  ll, err := egress.NewListener(ctx, l, tlsConfig)
  if err != nil {
    panic(err)
  }
  defer ll.Close()

  conf := &socks5.Config{}
  proxy, err := socks5.New(conf)
  if err != nil {
    panic(err)
  }

  common.Debugf("Starting SOCKS5 proxy...")

  err = proxy.Serve(ll)
  if err != nil {
    panic(err)
  }
}

```

### Signaling server

:arrow_right: [https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/signaling-server](signaling-server)

Deploy this to your favorite cloud hosting provider:

```
package main

import (
  "context"
  "fmt"
  "os"

  "github.com/getlantern/broflake/freddie"
)

func main() {
  port := os.Getenv("PORT")

  if port == "" {
    port = "9000"
  }

  tlsCertFile := os.Getenv("TLS_CERT_FILE")
  tlsKeyFile := os.Getenv("TLS_KEY_FILE")

  listenAddr := fmt.Sprintf(":%v", port)

  ctx := context.Background()
  f, err := freddie.New(ctx, listenAddr)

  if err != nil {
    panic(err)
  }

  if err = f.ListenAndServeTLS(tlsCertFile, tlsKeyFile); err != nil {
    panic(err)
  }
}

```

### Censored client

:arrow_right: [https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/censored-client](censored-client)

The censored client is the executable that your loved one will run on their computer. It exposes a local
proxy. A censored client, like the egress server, can support SOCKS5 or HTTP mode, but not both. Here
we create a SOCKS5 client.

You can further develop the censored client into a full fledged desktop app, with a user-friendly GUI.
Here, though, we demonstrate the minimum viable approach, which is a simple terminal application.

```
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
```

### Volunteer browser client
TODO