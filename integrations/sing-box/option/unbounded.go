package option

// Note that values which map to time.Duration in Unbounded's options structs are
// represented here as int (which will be converted to seconds). This means you
// can't set a time.Duration of 0, because we can't disambiguate it from an unset
// value. You also can't set any of the other int types to 0. But you probably
// shouldn't be setting any of this stuff to 0!
type UnboundedOutboundOptions struct {
	DialerOptions
	ServerOptions
	// BroflakeOptions
	CTableSize  int    `json:"c_table_size,omitempty"`
	PTableSize  int    `json:"p_table_size,omitempty"`
	BusBufferSz int    `json:"bus_buffer_sz,omitempty"`
	Netstated   string `json:"netstated,omitempty"`
	// WebRTCOptions
	DiscoverySrv      string `json:"discovery_srv,omitempty"`
	DiscoveryEndpoint string `json:"discovery_endpoint,omitempty"`
	GenesisAddr       string `json:"genesis_addr,omitempty"`
	NATFailTimeout    int    `json:"nat_fail_timeout,omitempty"`
	STUNBatchSize     int    `json:"stun_batch_size,omitempty"`
	Tag               string `json:"tag,omitempty"`
	Patience          int    `json:"patience,omitempty"`
	ErrorBackoff      int    `json:"error_backoff,omitempty"`
	ConsumerSessionID string `json:"consumer_session_id,omitempty"`
	// EgressOptions
	EgressAddr           string `json:"egress_addr,omitempty"`
	EgressEndpoint       string `json:"egress_endpoint,omitempty"`
	EgressConnectTimeout int    `json:"egress_connect_timeout,omitempty"`
	EgressErrorBackoff   int    `json:"egress_error_backoff,omitempty"`
}
