package egress

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A separate module and process prevent Go's module selection from replacing
// the legacy endpoint with the egress's newer QUIC dependency.
func TestConnectionManager_Migration_MixedVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("mixed-version integration test builds a legacy consumer")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("mixed-version integration test requires the Go toolchain")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "legacy-consumer")
	buildCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, goTool, "build", "-mod=readonly", "-o", binary, ".")
	build.Dir = filepath.Join("testdata", "legacy-consumer")
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build legacy consumer (requires cached modules or network access): %v\n%s", err, output)
	}
	cert := testServerTLS().Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) { testMixedVersionMigration(t, binary, certFile, keyFile, direction) })
	}
}

func testMixedVersionMigration(t *testing.T, binary, certFile, keyFile, direction string) {
	before := migrationSnapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, certFile, keyFile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(t.TempDir(), "consumer.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
		_ = logFile.Close()
		if t.Failed() {
			output, _ := os.ReadFile(logFile.Name())
			t.Logf("legacy consumer:\n%s", output)
		}
	})
	decoder := json.NewDecoder(stdout)
	encoder := json.NewEncoder(stdin)
	var ready struct{ Addr, Version string }
	if err := decoder.Decode(&ready); err != nil {
		t.Fatalf("legacy startup: %v", err)
	}
	const expectedVersion = "github.com/getlantern/quic-go-unbounded-fork@v0.59.0-unbounded"
	if ready.Version != expectedVersion {
		t.Fatalf("consumer built with %q, want %q", ready.Version, expectedVersion)
	}
	t.Logf("consumer: %s", ready.Version)
	addr, err := net.ResolveUDPAddr("udp", ready.Addr)
	if err != nil {
		t.Fatal(err)
	}
	cm := &connectionManager{connections: map[string]*connectionRecord{}, tlsConfig: testClientTLS(), migrationWindow: 5 * time.Second, probeTimeout: 5 * time.Second}
	path, disconnect := migrationDonor(t, addr)
	conn, _, _, err := cm.createOrMigrate("mixed-version", path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAllRecords(cm)
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(stream, prefix); err != nil {
		t.Fatal(err)
	}
	if string(prefix) != "ready" {
		t.Fatalf("unexpected prefix: %q", prefix)
	}
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{})
	for round := 0; round < 8; round++ {
		t.Logf("replacing donor %d/8", round+1)
		disconnect()
		select {
		case <-path.readError:
		case <-ctx.Done():
			t.Fatal("egress did not observe donor loss")
		}
		payload := bytes.Repeat([]byte(fmt.Sprintf("mixed-version transfer %d\n", round)), 65536)
		if err := stream.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(map[string]any{"Direction": direction, "Round": round}); err != nil {
			t.Fatal(err)
		}
		transfer := make(chan error, 1)
		go func() {
			if direction == "download" {
				_, err := stream.Write(payload)
				transfer <- err
			} else {
				got := make([]byte, len(payload))
				_, err := io.ReadFull(stream, got)
				if err == nil && !bytes.Equal(got, payload) {
					err = fmt.Errorf("upload payload corrupted")
				}
				transfer <- err
			}
		}()
		select {
		case err := <-transfer:
			t.Fatalf("transfer ended without a replacement donor: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		path, disconnect = migrationDonor(t, addr)
		migrated, _, _, err := cm.createOrMigrate("mixed-version", path)
		if err != nil {
			t.Fatalf("migration %d: %v", round+1, err)
		}
		if migrated != conn {
			t.Fatal("migration replaced the QUIC connection")
		}
		select {
		case err := <-transfer:
			if err != nil {
				t.Fatalf("transfer %d: %v", round+1, err)
			}
		case <-ctx.Done():
			t.Fatal("original stream did not resume")
		}
		var done struct{ Round int }
		if err := decoder.Decode(&done); err != nil {
			t.Fatalf("legacy transfer %d: %v", round+1, err)
		}
		if done.Round != round {
			t.Fatalf("legacy acknowledged round %d, want %d", done.Round, round)
		}
	}
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{8, 8, 0, 0, 0})
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("legacy consumer: %v", err)
	}
}
