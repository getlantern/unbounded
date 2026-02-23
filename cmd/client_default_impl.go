//go:build !wasm

// client_default_impl.go is the entry point for standalone builds for non-wasm build targets
package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strings"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

var (
	clientType = "desktop" // Must be "desktop" or "widget"
	proxyMode  = "socks5"  // Must be "socks5" or "http"
)

func main() {
	pprof := os.Getenv("PPROF")
	freddie := os.Getenv("FREDDIE")
	egress := os.Getenv("EGRESS")
	netstated := os.Getenv("NETSTATED")
	tag := os.Getenv("TAG")

	proxyPort := os.Getenv("PORT")
	if proxyPort == "" {
		proxyPort = "1080"
	}

	common.Debugf("Welcome to Broflake %v", common.Version)
	common.Debugf("clientType: %v", clientType)
	common.Debugf("freddie: %v", freddie)
	common.Debugf("egress: %v", egress)
	common.Debugf("netstated: %v", netstated)
	common.Debugf("tag: %v", tag)
	common.Debugf("pprof: %v", pprof)
	common.Debugf("proxyPort: %v", proxyPort)

	bfOpt := clientcore.NewDefaultBroflakeOptions()
	bfOpt.ClientType = clientType
	bfOpt.Netstated = netstated

	if clientType == "widget" {
		bfOpt.CTableSize = 5
		bfOpt.PTableSize = 5
	}

	rtcOpt := clientcore.NewDefaultWebRTCOptions()
	rtcOpt.Tag = tag

	if freddie != "" {
		rtcOpt.DiscoverySrv = freddie
	}

	egOpt := clientcore.NewDefaultEgressOptions()

	if egress != "" {
		egOpt.Addr = egress
	}

	// Load or generate persistent peer identity
	if id, err := loadOrGenerateIdentity(); err != nil {
		common.Debugf("Warning: failed to load/generate peer identity, using UUID: %v", err)
	} else {
		egOpt.SetIdentity(id)
		common.Debugf("PeerID (ed25519 public key): %v", egOpt.PeerID)
	}

	bfconn, _, err := clientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		log.Fatal(err)
	}

	if pprof != "" {
		go func() {
			common.Debug(http.ListenAndServe("localhost:"+pprof, nil))
		}()
	}

	if clientType == "desktop" {
		runLocalProxy(proxyPort, bfconn)
	}

	select {}
}

func identityFilePath() string {
	if p := os.Getenv("IDENTITY_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".unbounded", "identity.key")
	}
	return filepath.Join(home, ".unbounded", "identity.key")
}

func loadOrGenerateIdentity() (*clientcore.PeerIdentity, error) {
	path := identityFilePath()

	data, err := os.ReadFile(path)
	if err == nil {
		hexKey := strings.TrimSpace(string(data))
		return clientcore.PeerIdentityFromPrivateKeyHex(hexKey)
	}

	if !os.IsNotExist(err) {
		return nil, err
	}

	id, err := clientcore.NewPeerIdentity()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(id.PrivateKeyHex()+"\n"), 0600); err != nil {
		return nil, err
	}

	common.Debugf("Generated new peer identity, saved to %v", path)
	return id, nil
}
