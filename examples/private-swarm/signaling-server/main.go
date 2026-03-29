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
