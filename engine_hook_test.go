package meowcaller

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func testEngineForRawCallHook() (*engine, types.JID, types.JID) {
	ownID := types.JID{User: "111111111111111", Server: types.DefaultUserServer, Device: 9}
	ownLID := types.JID{User: "222222222222222", Server: types.HiddenUserServer, Device: 9}
	wa := whatsmeow.NewClient(&store.Device{ID: &ownID, LID: ownLID}, waLog.Noop)
	client := &Client{wa: wa, log: zerolog.Nop()}
	return newEngine(client), ownID, ownLID
}

func testIncomingOfferNode(caller types.JID) waBinary.Node {
	return waBinary.Node{
		Tag: "call",
		Attrs: waBinary.Attrs{
			"id":   "STANZA",
			"from": caller,
		},
		Content: []waBinary.Node{{
			Tag: "offer",
			Attrs: waBinary.Attrs{
				"call-id":      "CID",
				"call-creator": caller,
			},
		}},
	}
}

func TestOnCallRawSendsOfferReceipt(t *testing.T) {
	eng, ownID, ownLID := testEngineForRawCallHook()
	tests := []struct {
		name     string
		caller   types.JID
		wantFrom types.JID
	}{
		{name: "phone number caller", caller: types.JID{User: "333333333333333", Server: types.DefaultUserServer}, wantFrom: ownID},
		{name: "LID caller", caller: types.JID{User: "444444444444444", Server: types.HiddenUserServer}, wantFrom: ownLID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sent []waBinary.Node
			eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
				sent = append(sent, node)
				return nil
			}
			node := testIncomingOfferNode(tc.caller)

			if handled := eng.onCallRaw(&node); handled {
				t.Fatal("raw offer was fully handled; whatsmeow must still process it")
			}
			if len(sent) != 1 {
				t.Fatalf("sent %d nodes, want one offer receipt", len(sent))
			}
			receipt := sent[0]
			if receipt.Tag != "receipt" {
				t.Fatalf("sent tag %q, want receipt", receipt.Tag)
			}
			rag := receipt.AttrGetter()
			if got := rag.String("id"); got != "STANZA" {
				t.Fatalf("receipt id = %q, want STANZA", got)
			}
			if got := rag.JID("to"); got != tc.caller {
				t.Fatalf("receipt to = %s, want %s", got, tc.caller)
			}
			if got := rag.JID("from"); got != tc.wantFrom {
				t.Fatalf("receipt from = %s, want %s", got, tc.wantFrom)
			}
			children := receipt.GetChildren()
			if len(children) != 1 || children[0].Tag != "offer" {
				t.Fatalf("receipt children = %#v, want one offer", children)
			}
			oag := children[0].AttrGetter()
			if got := oag.String("call-id"); got != "CID" {
				t.Fatalf("receipt call-id = %q, want CID", got)
			}
			if got := oag.JID("call-creator"); got != tc.caller {
				t.Fatalf("receipt call-creator = %s, want %s", got, tc.caller)
			}
		})
	}
}

func TestOnCallRawSkipsEndedOfferReceipt(t *testing.T) {
	eng, _, _ := testEngineForRawCallHook()
	caller := types.JID{User: "333333333333333", Server: types.DefaultUserServer}
	tests := []struct {
		name  string
		attr  string
		value string
	}{
		{name: "ended flag", attr: "is_call_ended", value: "1"},
		{name: "terminate reason", attr: "terminate_reason", value: "accepted_elsewhere"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sent int
			eng.sendCallNode = func(_ context.Context, _ waBinary.Node) error {
				sent++
				return nil
			}
			node := testIncomingOfferNode(caller)
			node.GetChildren()[0].Attrs[tc.attr] = tc.value

			eng.onCallRaw(&node)

			if sent != 0 {
				t.Fatalf("sent %d nodes for ended offer, want none", sent)
			}
		})
	}
}

func TestOnCallRawSkipsMalformedOfferReceipt(t *testing.T) {
	eng, _, _ := testEngineForRawCallHook()
	caller := types.JID{User: "333333333333333", Server: types.DefaultUserServer}
	tests := []struct {
		name   string
		mutate func(*waBinary.Node)
	}{
		{name: "missing stanza id", mutate: func(node *waBinary.Node) { delete(node.Attrs, "id") }},
		{name: "missing caller", mutate: func(node *waBinary.Node) { delete(node.Attrs, "from") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sent int
			eng.sendCallNode = func(_ context.Context, _ waBinary.Node) error {
				sent++
				return nil
			}
			node := testIncomingOfferNode(caller)
			tc.mutate(&node)

			eng.onCallRaw(&node)

			if sent != 0 {
				t.Fatalf("sent %d nodes for malformed offer, want none", sent)
			}
		})
	}
}

func TestOnCallRawMuteStillConsumesPendingAccept(t *testing.T) {
	eng, _, _ := testEngineForRawCallHook()
	caller := types.JID{User: "333333333333333", Server: types.DefaultUserServer}
	eng.calls["CID"] = &engineCall{
		direction:     CallDirectionIncoming,
		from:          caller,
		creator:       caller,
		acceptPending: true,
	}
	node := waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": caller},
		Content: []waBinary.Node{{
			Tag: "mute_v2",
			Attrs: waBinary.Attrs{
				"call-id":      "CID",
				"call-creator": caller,
				"mute-state":   "1",
			},
		}},
	}

	eng.onCallRaw(&node)

	eng.mu.Lock()
	pending := eng.calls["CID"].acceptPending
	eng.mu.Unlock()
	if pending {
		t.Fatal("mute_v2 did not consume the pending accept")
	}
}

func TestInstallCallAckHookMatchesPinnedUpstreamLayout(t *testing.T) {
	wa := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	client := &Client{wa: wa, log: zerolog.Nop()}
	eng := newEngine(client)
	if err := eng.installCallAckHook(); err != nil {
		t.Fatalf("install raw call adapter: %v", err)
	}
}

func TestInstallCallAckHookRejectsMissingClient(t *testing.T) {
	client := &Client{log: zerolog.Nop()}
	eng := newEngine(client)
	if err := eng.installCallAckHook(); err == nil {
		t.Fatal("raw call adapter accepted a missing whatsmeow client")
	}
}

func TestGroupFeaturesRejectUnavailableRawAdapter(t *testing.T) {
	client := &Client{log: zerolog.Nop()}
	eng := newEngine(client)
	eng.rawCallHookErr = errors.New("upstream layout changed")
	if _, err := eng.placeGroupCall(
		context.Background(),
		[]string{"1", "2"},
		GroupCallOptions{},
	); err == nil {
		t.Fatal("group call continued without its raw call adapter")
	}
}
