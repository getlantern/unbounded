package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/quic-go/quic-go"
)

type connectionRecord struct {
	mx           sync.Mutex
	connection   *quic.Connection
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
		(*record.connection).CloseWithError(quic.ApplicationErrorCode(42069), "expired before migration")
		nQUICConnectionsCounter.Add(context.Background(), -1)
		delete(manager.connections, csid)
		common.Debugf("QUIC connection for CSID %v expired, closed, and deleted", csid)
	}

	record.mx.Unlock()
	manager.mx.Unlock()
}

func (manager *connectionManager) createOrMigrate(csid string, pconn *errorlessWebSocketPacketConn) (*quic.Connection, error) {
	manager.mx.Lock()

	transport := &quic.Transport{Conn: pconn}
	record, ok := manager.connections[csid]

	// Atomic creation path
	if !ok {
		common.Debugf("No existing QUIC connection for %v [CSID: %v], dialing...", pconn.addr, csid)
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

		nQUICConnectionsCounter.Add(context.Background(), 1)
		common.Debugf("%v dialed a new QUIC connection!", pconn.addr)
		manager.connections[csid] = &connectionRecord{connection: &newConn, lastMigrated: time.Now()}
		manager.mx.Unlock()
		return &newConn, nil
	}

	// Atomic migration path
	common.Debugf("Trying to migrate QUIC connection for %v [CSID %v]", pconn.addr, csid)
	t1 := time.Now()
	record.mx.Lock()
	manager.mx.Unlock()
	defer record.mx.Unlock()

	path, err := (*record.connection).AddPath(transport)
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
	common.Debugf("Migrated a QUIC connection to %v! (took %vs)", pconn.addr, t2.Sub(t1).Seconds())
	record.lastMigrated = time.Now()

	if record.lastPath != nil {
		err = (*record.lastPath).Close()

		// If we encounter an error closing the last path, we still proceed with a successful migration
		if err != nil {
			common.Debugf("Error closing last path for %v: %v", pconn.addr, err)
		} else {
			common.Debugf("Closed old path for %v", pconn.addr)
		}
	}

	record.lastPath = path
	return record.connection, nil
}
