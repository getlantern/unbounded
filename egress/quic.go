package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/quic-go/quic-go"
)

type connectionRecord struct {
	mx           sync.Mutex
	connection   *quic.Conn
	lastMigrated time.Time
	lastPath     *quic.Path
}

// migrationWindow: when migrating from WebSocket A to B, how long should we wait after WebSocket
// A goes away for WebSocket B to appear, before giving up and deleting the QUIC connection state?
// Setting this value larger than your quic.Config's MaxIdleTimeout will break things, because that
// will create scenarios where we attempt to migrate a QUIC connection that has timed out and closed.

// probeTimeout: during migration, how long should we wait for a probe response before giving up?
// This should be set to a larger value than migrationWindow. This ensures that upon migration
// failure, QUIC connection state is deleted from the connection manager before a second attempt
// is made.
type connectionManager struct {
	mx              sync.Mutex
	connections     map[string]*connectionRecord
	tlsConfig       *tls.Config
	migrationWindow time.Duration
	probeTimeout    time.Duration
}

func (manager *connectionManager) deleteIfNotMigratedSince(csid string, t time.Time) {
	manager.mx.Lock()

	record, ok := manager.connections[csid]

	if !ok {
		manager.mx.Unlock()
		return
	}

	record.mx.Lock()

	if !record.lastMigrated.After(t) {
		record.connection.CloseWithError(quic.ApplicationErrorCode(42069), "expired before migration")
		delete(manager.connections, csid)
		slog.Debug("QUIC connection expired, closed, and deleted", "csid", csid, "total", atomic.AddUint64(&nQUICConnections, ^uint64(0)))
	}

	record.mx.Unlock()
	manager.mx.Unlock()
}

// createOrMigrate accepts any net.PacketConn for the new transport. In
// production this is always *errorlessWebSocketPacketConn (the WS-as-UDP
// adapter), but tests inject in-memory or loopback-UDP pconns to exercise
// the connection-migration paths without a real WebSocket handshake.
func (manager *connectionManager) createOrMigrate(csid string, pconn net.PacketConn) (*quic.Conn, error) {
	manager.mx.Lock()

	transport := &quic.Transport{Conn: pconn}
	record, ok := manager.connections[csid]

	// Atomic creation path
	if !ok {
		slog.Debug("No existing QUIC connection, dialing...", "local_addr", pconn.LocalAddr(), "csid", csid)
		newConn, err := transport.Dial(
			context.Background(),
			common.DebugAddr("NELSON WUZ HERE"),
			manager.tlsConfig,
			&common.QUICCfg,
		)

		if err != nil {
			manager.mx.Unlock()
			return nil, err
		}
		slog.Debug("Dialed a new QUIC connection!", "local_addr", pconn.LocalAddr(), "total", atomic.AddUint64(&nQUICConnections, uint64(1)))
		manager.connections[csid] = &connectionRecord{connection: newConn, lastMigrated: time.Now()}
		manager.mx.Unlock()
		return newConn, nil
	}
	// Atomic migration path
	slog.Debug("Trying to migrate QUIC connection", "local_addr", pconn.LocalAddr(), "csid", csid)
	t1 := time.Now()
	record.mx.Lock()
	manager.mx.Unlock()
	defer record.mx.Unlock()

	path, err := record.connection.AddPath(transport)
	if err != nil {
		return nil, fmt.Errorf("AddPath error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), manager.probeTimeout)
	defer cancel()
	err = path.Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("path probe error: %v", err)
	}

	err = path.Switch()
	if err != nil {
		return nil, fmt.Errorf("path switch error: %v", err)
	}

	t2 := time.Now()
	slog.Debug("Migrated a QUIC connection", "local_addr", pconn.LocalAddr(), "duration_s", t2.Sub(t1).Seconds())
	record.lastMigrated = time.Now()

	if record.lastPath != nil {
		err = record.lastPath.Close()

		// If we encounter an error closing the last path, we still proceed with a successful migration
		if err != nil {
			slog.Debug("Error closing last path", "local_addr", pconn.LocalAddr(), "error", err)
		} else {
			slog.Debug("Closed old path", "local_addr", pconn.LocalAddr())
		}
	}

	record.lastPath = path
	return record.connection, nil
}
