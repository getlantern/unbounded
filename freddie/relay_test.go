package freddie

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// Freddie notes whether each peer offered a relay candidate, in either message form.
func TestClassifyMessage_NotesRelayCandidates(t *testing.T) {
	answer := sdpJSON(t,
		"a=candidate:1 1 udp 1686052607 203.0.113.7 40001 typ srflx raddr 10.0.0.2 rport 50000 generation 0",
		"a=candidate:2 1 udp 41885439 198.51.100.9 61000 typ relay raddr 203.0.113.7 rport 40001 generation 0",
	)
	n, ok := classifyMessage(common.SignalMsgAnswer, answer)
	require.True(t, ok)
	assert.True(t, n.relay)
	assert.Equal(t, "yes", n.relayLabel())

	ice, err := json.Marshal(common.ICEMsg{Candidates: []webrtc.ICECandidate{{
		Typ: webrtc.ICECandidateTypeSrflx, Protocol: webrtc.ICEProtocolUDP,
		Address: "198.51.100.4", Port: 61000, RelatedAddress: "10.0.0.2", RelatedPort: 50000,
	}}})
	require.NoError(t, err)
	n, ok = classifyMessage(common.SignalMsgICE, string(ice))
	require.True(t, ok)
	assert.False(t, n.relay)
	assert.Equal(t, "no", n.relayLabel())
}

// The relay labels follow the attempt from the offer cache onto its outcome, with the NAT labels'
// none, unknown and unreported.
func TestAttemptConsumerAttrs_RelayLabels(t *testing.T) {
	f := &Freddie{offerConsumers: newOfferConsumerCache(time.Now), roomGeneses: newRoomGenesisSet(time.Now)}
	f.offerConsumers.add("offer", offerConsumer{volunteerNAT: outcomeNATNone, consumerNAT: outcomeNATNone,
		volunteerRelay: outcomeNATNone, consumerRelay: outcomeNATNone})
	f.offerConsumers.answered("offer", "answer", "port_changed", "no")
	f.offerConsumers.consumerICE("answer", "port_changed", "yes")

	got := map[string]string{}
	for _, kv := range f.attemptConsumerAttrs("offer") {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, "no", got["volunteer_relay"])
	assert.Equal(t, "yes", got["consumer_relay"])

	got = map[string]string{}
	for _, kv := range f.attemptConsumerAttrs("forgotten") {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, "unknown", got["volunteer_relay"])

	f.offerConsumers.add("unanswered", offerConsumer{volunteerNAT: outcomeNATNone, consumerNAT: outcomeNATNone,
		volunteerRelay: outcomeNATNone, consumerRelay: outcomeNATNone})
	got = map[string]string{}
	for _, kv := range f.attemptConsumerAttrs("unanswered") {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, outcomeNATNone, got["consumer_relay"], "a consumer that never sent candidates")
}

// The producer's TURN result, gather cut and selected pair become outcome labels, normalized.
func TestRecordProducerOutcome_LabelsTURNGatherAndPair(t *testing.T) {
	form := url.Values{
		common.PrevOutcomeField:   {common.OutcomeConnected},
		common.PrevTURNField:      {common.TURNMissing},
		common.PrevGatherCutField: {common.GatherCut},
		common.PrevPairField:      {"srflx-relay"},
	}
	got := outcomeLabels(t, form, common.Version, "turn", "gather_cut", "pair")
	assert.Equal(t, map[string]int64{"missing/cut/srflx-relay": 1}, got)

	got = outcomeLabels(t, url.Values{common.PrevOutcomeField: {common.OutcomeNATTimeout}}, common.Version, "turn", "gather_cut", "pair")
	assert.Equal(t, map[string]int64{"unreported/unreported/unreported": 1}, got, "a producer that predates the fields")

	got = outcomeLabels(t, url.Values{
		common.PrevOutcomeField: {common.OutcomeNATTimeout},
		common.PrevTURNField:    {common.TURNNone},
	}, common.Version, "turn", "gather_cut")
	assert.Equal(t, map[string]int64{"none/complete": 1}, got)
}
