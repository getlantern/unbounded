package egress

import (
	"context"

	"github.com/getlantern/broflake/common"
	"github.com/go-redis/redis/v8"
)

var rc *redis.Client

const reportIngressKey = "ingress"
const reportEgressKey = "egress"

// Write the number of bytes to redis, if a client was given when starting the server, otherwise do nothing
func recordBytes(transferType, userId string, nBytes int64) {
	common.Debugf("WebSocket connection: user: '%v' transferred(%v) %v bytes", userId, transferType, nBytes)
	if rc == nil {
		return
	}
	ctx := context.Background()
	err := rc.HIncrBy(ctx, userId, transferType, nBytes).Err()
	if err != nil {
		common.Debugf("Error: recording bytes to redis: %v, user-id: %v, nBytes: %v", err, userId, nBytes)
		return
	}
}
