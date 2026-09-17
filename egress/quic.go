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

func (manager *connectionManager) deleteIfNotMigratedSince(csid string, t time.Time) {
	manager.mx.Lock()
	record := manager.connections[csid]
	manager.mx.Unlock()
	if record == nil {
		return
	}
	record.mx.Lock()
	defer record.mx.Unlock()
	manager.mx.Lock()
	expired := manager.connections[csid] == record && record.connection != nil && !record.lastMigrated.After(t)
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
func (manager *connectionManager) createOrMigrate(csid string, pconn net.PacketConn) (*quic.Conn, error) {
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
			return nil, err
		}
		slog.Debug("Dialed a new QUIC connection!", "local_addr", pconn.LocalAddr(), "total", atomic.AddUint64(&nQUICConnections, uint64(1)))
		record.connection = newConn
		record.lastMigrated = time.Now()
		return newConn, nil
	}
	// Atomic migration path
	recordMigration(migrationAttempt)
	slog.Debug("Trying to migrate QUIC connection", "local_addr", pconn.LocalAddr(), "csid", csid)
	t1 := time.Now()

	path, err := record.connection.AddPath(transport)
	if err != nil {
		recordMigration(migrationAddPathError)
		return nil, fmt.Errorf("AddPath error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), manager.probeTimeout)
	defer cancel()
	err = path.Probe(ctx)
	if err != nil {
		if closeErr := path.Close(); closeErr != nil {
			slog.Debug("Error closing failed migration path", "local_addr", pconn.LocalAddr(), "error", closeErr)
		}
		recordMigration(migrationProbeError)
		return nil, fmt.Errorf("path probe error: %v", err)
	}

	err = path.Switch()
	if err != nil {
		if closeErr := path.Close(); closeErr != nil {
			slog.Debug("Error closing failed migration path", "local_addr", pconn.LocalAddr(), "error", closeErr)
		}
		recordMigration(migrationSwitchError)
		return nil, fmt.Errorf("path switch error: %v", err)
	}

	t2 := time.Now()
	recordMigration(migrationSuccess)
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
