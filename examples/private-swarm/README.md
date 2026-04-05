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

:arrow_right: [egress-server](https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/egress-server)

Unbounded supports two proxy modes: HTTP or SOCKS5. An egress server can support one mode or the other, but not both. Here we create a SOCKS5 egress server. (See [here](https://github.com/getlantern/unbounded/blob/main/egress/cmd/http) for an HTTP mode example.)

Deploy this to your favorite cloud hosting provider:

```golang
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

:arrow_right: [signaling-server](https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/signaling-server)

Deploy this to your favorite cloud hosting provider:

```golang
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

:arrow_right: [censored-client](https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/censored-client)

The censored client is the executable that your loved one will run on their computer. It exposes a local proxy. A censored client, like the egress server, can support SOCKS5 or HTTP mode, but not both. Here we create a SOCKS5 client.

You can further develop the censored client into a full fledged desktop app, with a user-friendly GUI. Here, though, we demonstrate the minimum viable approach, which is a simple terminal application.

Provide the URL for your signaling server and egress server as the `FREDDIE` and `EGRESS` env vars, respectively.

```golang
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

:arrow_right: [volunteer-client](https://github.com/getlantern/unbounded/blob/main/examples/private-swarm/volunteer-client)

The volunteer client is a static web page which operationalizes a WebAssembly build of the proxy engine. The WebAssembly proxy engine exposes JavaScript bindings which can be used to create a robust interactive web UI.

Build the WebAssembly proxy engine like so: `cd cmd && ./build_web.sh`

Now you can find `widget.wasm` and `wasm_exec.js` in `cmd/dist/public`.

`CTableSize` and `PTableSize` control the client concurrency. In this example, we set `CTableSize` and `PTableSize` to 1, because there's just a single loved one we're helping to unblock. To create a private swarm intended to help multiple people simultaneously, increase both `CTableSize` and 
`PTableSize` to a larger integer. (Browser restrictions on concurrent HTTP requests may result in strange behavior for values larger than 5.)

Set your signaling server and egress server URL, deploy this web page, and distribute the link to your network of volunteers. Keep it running in a background tab while you're working or scrolling to participate in the swarm!

```html
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8"/>
  <title>Unbounded Volunteer Client</title>
  <script src="wasm_exec.js"></script>
  <link rel="stylesheet" href="style.css"/>
</head>
<body>
  <h1>Unbounded</h1>
  <h2>Volunteer client &mdash; help unblock the internet</h2>

  <fieldset>
    <legend>configuration</legend>
    <label>Signaling server
      <input type="text" id="cfg-signaling" value="https://your-signaling-server:9000" style="width:380px"/>
    </label>
    <label>Egress server
      <input type="text" id="cfg-egress" value="https://your-egress-server:8000" style="width:380px"/>
    </label>
  </fieldset>

  <div style="margin-bottom:16px">
    <button id="btn-start" disabled>Start</button>
    <button id="btn-stop" disabled>Stop</button>
    <span id="state-text" style="margin-left:16px;font-size:13px;color:#888">
      <span id="state-indicator"></span>loading wasm...
    </span>
  </div>

  <div id="status-bar">
    <span class="stat"><span class="stat-label">people you're helping: </span><span class="stat-value" id="stat-consumers">0</span></span>
    <span class="stat"><span class="stat-label">throughput: </span><span class="stat-value" id="stat-throughput">&mdash;</span></span>
  </div>

  <table id="consumers">
    <thead><tr><th>slot</th><th>addr</th><th>status</th></tr></thead>
    <tbody id="consumers-body"></tbody>
  </table>

  <div id="log"></div>

  <script>
    let broflake = null;
    let running = false;
    let generation = 0;
    const consumerSlots = {};

    const btnStart = document.getElementById('btn-start');
    const btnStop = document.getElementById('btn-stop');
    const stateText = document.getElementById('state-text');
    const stateIndicator = document.getElementById('state-indicator');
    const statConsumers = document.getElementById('stat-consumers');
    const statThroughput = document.getElementById('stat-throughput');
    const consumersBody = document.getElementById('consumers-body');
    const logEl = document.getElementById('log');

    function log(msg, cls) {
      const d = document.createElement('div');
      d.className = cls || 'log-info';
      d.textContent = '[' + new Date().toLocaleTimeString() + '] ' + msg;
      logEl.appendChild(d);
      logEl.scrollTop = logEl.scrollHeight;
    }

    function formatBytes(b) {
      if (b < 1024) return b + ' B/s';
      if (b < 1048576) return (b / 1024).toFixed(1) + ' KB/s';
      return (b / 1048576).toFixed(2) + ' MB/s';
    }

    function updateConsumerTable() {
      consumersBody.innerHTML = '';
      let active = 0;
      for (const [slot, info] of Object.entries(consumerSlots)) {
        const tr = document.createElement('tr');
        const connected = info.state === 1;
        if (connected) active++;
        tr.innerHTML =
          '<td>' + slot + '</td>' +
          '<td>' + (connected ? info.addr || '(unknown)' : '&mdash;') + '</td>' +
          '<td class="' + (connected ? 'connected' : 'disconnected') + '">' +
          (connected ? 'connected' : 'idle') + '</td>';
        consumersBody.appendChild(tr);
      }
      statConsumers.textContent = active;
    }

    function setState(s) {
      stateIndicator.className = s;
      if (s === 'ready') stateText.innerHTML = '<span id="state-indicator" class="ready"></span>ready';
      if (s === 'running') stateText.innerHTML = '<span id="state-indicator" class="running"></span>running';
      if (s === 'idle') stateText.innerHTML = '<span id="state-indicator"></span>idle';
    }

    // Rebuild stateIndicator ref after innerHTML swap
    function getStateIndicator() { return document.getElementById('state-indicator'); }

    function attachEvents(bf) {
      const myGen = generation;

      bf.addEventListener('ready', function() {
        if (myGen !== generation) return;
        log('unbounded ready', 'log-event');
        btnStart.disabled = false;
        btnStop.disabled = true;
        setState('ready');
      });

      bf.addEventListener('downstreamThroughput', function(e) {
        if (myGen !== generation) return;
        statThroughput.textContent = formatBytes(e.detail && e.detail.bytesPerSec || 0);
      });

      bf.addEventListener('consumerConnectionChange', function(e) {
        if (myGen !== generation) return;
        const { state, workerIdx, addr } = e.detail;
        consumerSlots[workerIdx] = { state, addr };
        updateConsumerTable();
        if (state === 1) {
          log('someone connected' + (addr ? ' from ' + addr : ''), 'log-event');
        } else {
          log('someone disconnected', 'log-info');
        }
      });
    }

    btnStart.addEventListener('click', function() {
      const signaling = document.getElementById('cfg-signaling').value;
      const egress = document.getElementById('cfg-egress').value;

      generation++;
      Object.keys(consumerSlots).forEach(k => delete consumerSlots[k]);
      broflake = newBroflake(
        'widget',     // ClientType
        1,            // CTableSize
        1,            // PTableSize
        4096,         // BusBufferSz
        '',           // Netstated
        signaling,    // DiscoverySrv
        '/v1/signal', // WebRTC endpoint
        2,            // STUNBatchSize
        '',           // Tag
        egress,       // EgressOptions.Addr
        '/ws',        // EgressOptions.Endpoint
      );

      if (!broflake) {
        log('failed to create broflake instance', 'log-error');
        return;
      }

      // ready fires synchronously inside newBroflake() before we can attach listeners,
      // so the 'ready' listener here only matters for stop/start cycles on this instance.
      attachEvents(broflake);
      broflake.start();
      running = true;
      btnStart.disabled = true;
      btnStop.disabled = false;
      setState('running');
      log('started', 'log-info');
    });

    btnStop.addEventListener('click', function() {
      if (!broflake) return;
      broflake.stop();
      running = false;
      btnStart.disabled = true;
      btnStop.disabled = true;
      statThroughput.textContent = '\u2014';
      setState('idle');
      log('stopped \u2014 waiting for ready event before restart is allowed', 'log-info');
    });

    // Load and start the wasm module
    const go = new Go();
    WebAssembly.instantiateStreaming(fetch('widget.wasm'), go.importObject)
      .then(function(result) {
        go.run(result.instance);

        log('unbounded ready', 'log-event');
        btnStart.disabled = false;
        setState('ready');
      })
      .catch(function(err) {
        log('failed to load wasm: ' + err, 'log-error');
        stateText.textContent = 'error loading wasm';
      });
  </script>
</body>
</html>

```