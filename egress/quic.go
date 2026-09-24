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
	mx         sync.Mutex
	connection *quic.Conn
	transport  *quic.Transport
	lastPath   *quic.Path
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

// lockRecord serializes one session without holding the map lock while waiting.
// Recheck membership after taking the record lock: expiry or a failed dial may
// have removed this record while we waited.
func (manager *connectionManager) lockRecord(csid string) *connectionRecord {
	for {
		manager.mx.Lock()
		record := manager.connections[csid]
		if record == nil {
			record = &connectionRecord{}
			manager.connections[csid] = record
		}
		manager.mx.Unlock()
		record.mx.Lock()
		manager.mx.Lock()
		current := manager.connections[csid] == record
		manager.mx.Unlock()
		if current {
			return record
		}
		record.mx.Unlock()
	}
}

// deleteIfCurrent ignores cleanup from superseded connections or donor transports.
func (manager *connectionManager) deleteIfCurrent(csid string, conn *quic.Conn, donor *quic.Transport) {
	if donor == nil {
		return
	}
	manager.deleteConnection(csid, conn, donor)
}

// deleteOnQUICFailure removes only this connection, regardless of its current donor.
func (manager *connectionManager) deleteOnQUICFailure(csid string, conn *quic.Conn) {
	manager.deleteConnection(csid, conn, nil)
}

// Only QUIC-failure cleanup may omit the donor identity.
func (manager *connectionManager) deleteConnection(csid string, conn *quic.Conn, donor *quic.Transport) {
	manager.mx.Lock()
	record := manager.connections[csid]
	manager.mx.Unlock()
	if record == nil {
		return
	}
	record.mx.Lock()
	defer record.mx.Unlock()
	manager.mx.Lock()
	expired := manager.connections[csid] == record && record.connection != nil && record.connection == conn && (donor == nil || record.transport == donor)
	if expired {
		delete(manager.connections, csid)
	}
	manager.mx.Unlock()
	if expired {
		record.connection.CloseWithError(quic.ApplicationErrorCode(42069), "expired before migration")
		slog.Debug("QUIC connection expired, closed, and deleted", "csid", csid, "total", atomic.AddUint64(&nQUICConnections, ^uint64(0)))
	}
}

// createOrMigrate accepts any net.PacketConn for the new transport. In
// production this is always *errorlessWebSocketPacketConn (the WS-as-UDP
// adapter), but tests inject in-memory or loopback-UDP pconns to exercise
// the connection-migration paths without a real WebSocket handshake.
//
// The bool reports whether the migrate branch was taken. Only the caller
// can tell the two apart otherwise, and they differ in a way that
// matters outside this function: a migrated connection keeps the streams
// it already had, so nothing downstream will observe a fresh one.
func (manager *connectionManager) createOrMigrate(csid string, pconn net.PacketConn) (*quic.Conn, *quic.Transport, bool, error) {
	record := manager.lockRecord(csid)
	defer record.mx.Unlock()
	transport := &quic.Transport{Conn: pconn}

	// Atomic creation path
	if record.connection == nil {
		slog.Debug("No existing QUIC connection, dialing...", "local_addr", pconn.LocalAddr(), "csid", csid)
		newConn, err := transport.Dial(
			context.Background(),
			common.DebugAddr("NELSON WUZ HERE"),
			manager.tlsConfig,
			&common.QUICCfg,
		)

		if err != nil {
			manager.mx.Lock()
			delete(manager.connections, csid)
			manager.mx.Unlock()
			return nil, nil, false, err
		}
		slog.Debug("Dialed a new QUIC connection!", "local_addr", pconn.LocalAddr(), "total", atomic.AddUint64(&nQUICConnections, uint64(1)))
		record.connection = newConn
		record.transport = transport
		return newConn, transport, false, nil
	}
	// Atomic migration path
	recordMigration(migrationAttempt)
	slog.Debug("Trying to migrate QUIC connection", "local_addr", pconn.LocalAddr(), "csid", csid)
	t1 := time.Now()

	path, err := record.connection.AddPath(transport)
	if err != nil {
		recordMigration(migrationAddPathError)
		return nil, nil, false, fmt.Errorf("AddPath error: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), manager.probeTimeout)
	defer cancel()
	err = path.Probe(ctx)
	if err != nil {
		if closeErr := path.Close(); closeErr != nil {
			slog.Debug("Error closing failed migration path", "local_addr", pconn.LocalAddr(), "error", closeErr)
		}
		recordMigration(migrationProbeError)
		return nil, nil, false, fmt.Errorf("path probe error: %w", err)
	}

	err = path.Switch()
	if err != nil {
		if closeErr := path.Close(); closeErr != nil {
			slog.Debug("Error closing failed migration path", "local_addr", pconn.LocalAddr(), "error", closeErr)
		}
		recordMigration(migrationSwitchError)
		return nil, nil, false, fmt.Errorf("path switch error: %w", err)
	}

	t2 := time.Now()
	recordMigration(migrationSuccess)
	slog.Debug("Migrated a QUIC connection", "local_addr", pconn.LocalAddr(), "duration_s", t2.Sub(t1).Seconds())
	record.transport = transport

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
	return record.connection, transport, true, nil
}
