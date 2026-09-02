package meowcaller

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type lifecycleAudioSource struct {
	closeCount int
}

func (s *lifecycleAudioSource) ReadFrame() ([]float32, error) {
	return nil, errors.New("unused")
}

func (s *lifecycleAudioSource) Close() error {
	s.closeCount++
	return nil
}

type lifecycleAudioSink struct {
	closeCount int
}

func (s *lifecycleAudioSink) WriteFrame([]float32) error {
	return nil
}

func (s *lifecycleAudioSink) Close() error {
	s.closeCount++
	return nil
}

func testEngineWithOutgoingCall() (*engine, *Call) {
	c := &Client{}
	c.eng = newEngine(c)
	call := &Call{eng: c.eng, id: "CID", peer: peerJID(), phase: CallPhaseCalling}
	c.eng.calls["CID"] = &engineCall{
		call:        call,
		direction:   CallDirectionOutgoing,
		from:        peerJID(),
		creator:     creatorJID(),
		localVideo:  true,
		remoteVideo: true,
	}
	return c.eng, call
}

func testEngineWithIncomingCall() (*engine, *Call) {
	c := &Client{}
	c.eng = newEngine(c)
	call := &Call{eng: c.eng, id: "CID", peer: peerJID(), phase: CallPhaseRinging}
	from := peerJID()
	from.Device = 7
	c.eng.calls[call.ID()] = &engineCall{
		call:      call,
		direction: CallDirectionIncoming,
		from:      from,
		creator:   creatorJID(),
	}
	return c.eng, call
}

func setTestOwnID(eng *engine) types.JID {
	ownID := types.JID{User: "111111111111111", Server: types.DefaultUserServer, Device: 9}
	eng.c.wa = &whatsmeow.Client{Store: &store.Device{ID: &ownID}}
	return ownID
}

func videoStateNode(state int) *waBinary.Node {
	return &waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"call-id": "CID", "state": strconv.Itoa(state),
	}}
}

func senderVideoState(sender *videoSender) (active, gated bool) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.active, sender.sendGated
}

func TestCallVideoUpgradeGatesUntilPeerAcceptAndCanStop(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.videoTx = &videoSender{}
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = append(sent, node)
		return nil
	}

	if err := call.StartVideo(); err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	if len(sent) != 1 || sent[0].GetChildren()[0].AttrGetter().Int("state") != signaling.VideoStateUpgradeRequestV2 {
		t.Fatalf("StartVideo sent %#v, want one state=11 stanza", sent)
	}
	if orientation, ok := sent[0].GetChildren()[0].Attrs["device_orientation"]; !ok || orientation != "0" {
		t.Fatalf("StartVideo orientation = %v, want explicit 0", orientation)
	}
	if active, gated := senderVideoState(m.videoTx); !active || !gated {
		t.Fatalf("upgrade sender state = active:%v gated:%v, want true,true", active, gated)
	}

	eng.onVideoStanza(videoStateNode(signaling.VideoStateUpgradeAccept))
	if len(sent) != 2 || sent[1].GetChildren()[0].AttrGetter().Int("state") != signaling.VideoStateEnabled {
		t.Fatalf("peer acceptance sent %#v, want state=1 announcement after state=4", sent)
	}
	if orientation, ok := sent[1].GetChildren()[0].Attrs["device_orientation"]; !ok || orientation != "0" {
		t.Fatalf("enabled announcement orientation = %v, want explicit 0", orientation)
	}
	if active, gated := senderVideoState(m.videoTx); !active || gated {
		t.Fatalf("accepted sender state = active:%v gated:%v, want true,false", active, gated)
	}

	if err := call.StopVideo(); err != nil {
		t.Fatalf("StopVideo: %v", err)
	}
	if len(sent) != 3 || sent[2].GetChildren()[0].AttrGetter().Int("state") != signaling.VideoStateStopped {
		t.Fatalf("StopVideo sent %#v, want trailing state=6 stanza", sent)
	}
	if active, _ := senderVideoState(m.videoTx); active {
		t.Fatal("video sender remained active after StopVideo")
	}
	if call.IsSendingVideo() {
		t.Fatal("call remained marked as sending video after StopVideo")
	}
	if !call.IsReceivingVideo() || !call.IsVideo() {
		t.Fatal("stopping local video also stopped the peer video flow")
	}
}

func TestSetVideoEnabledOnlyTogglesLocalFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.videoTx = &videoSender{active: true}
	var states []int
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		states = append(states, node.GetChildren()[0].AttrGetter().Int("state"))
		return nil
	}

	if err := call.SetVideoEnabled(false); err != nil {
		t.Fatalf("SetVideoEnabled(false): %v", err)
	}
	if active, _ := senderVideoState(m.videoTx); active {
		t.Fatal("disabling local video left sender active")
	}
	if call.IsSendingVideo() || !call.IsReceivingVideo() {
		t.Fatalf("disabled state = send:%v receive:%v, want false,true", call.IsSendingVideo(), call.IsReceivingVideo())
	}

	if err := call.SetVideoEnabled(true); err != nil {
		t.Fatalf("SetVideoEnabled(true): %v", err)
	}
	if active, gated := senderVideoState(m.videoTx); !active || gated {
		t.Fatalf("enabled sender state = active:%v gated:%v, want true,false", active, gated)
	}
	if !call.IsSendingVideo() || !call.IsReceivingVideo() {
		t.Fatalf("enabled state = send:%v receive:%v, want true,true", call.IsSendingVideo(), call.IsReceivingVideo())
	}
	if len(states) != 2 || states[0] != signaling.VideoStateDisabled || states[1] != signaling.VideoStateEnabled {
		t.Fatalf("toggle states = %v, want [0 1]", states)
	}
}

func TestCallAcceptVideoPreservesDisabledLocalFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.peerVideoUpgrade = true
	m.videoTx = &videoSender{}
	var states []int
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		states = append(states, node.GetChildren()[0].AttrGetter().Int("state"))
		return nil
	}

	if err := call.AcceptVideo(); err != nil {
		t.Fatalf("AcceptVideo: %v", err)
	}
	if len(states) != 2 || states[0] != signaling.VideoStateStopped || states[1] != signaling.VideoStateUpgradeAccept {
		t.Fatalf("AcceptVideo states = %v, want [6 4]", states)
	}
	if active, gated := senderVideoState(m.videoTx); active || gated {
		t.Fatalf("accepted sender state = active:%v gated:%v, want false,false", active, gated)
	}
	if call.IsSendingVideo() {
		t.Fatal("accepting peer video enabled the local sender")
	}
}

func TestCallAcceptVideoPreservesEnabledLocalFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.peerVideoUpgrade = true
	m.videoTx = &videoSender{active: true}
	var states []int
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		states = append(states, node.GetChildren()[0].AttrGetter().Int("state"))
		return nil
	}

	if err := call.AcceptVideo(); err != nil {
		t.Fatalf("AcceptVideo: %v", err)
	}
	if len(states) != 1 || states[0] != signaling.VideoStateUpgradeAccept {
		t.Fatalf("AcceptVideo states = %v, want [4]", states)
	}
	if active, gated := senderVideoState(m.videoTx); !active || gated {
		t.Fatalf("accepted sender state = active:%v gated:%v, want true,false", active, gated)
	}
}

func TestInboundVideoStopOnlyDisablesRemoteFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.videoTx = &videoSender{active: true}
	var got VideoState
	call.OnVideoState(func(state VideoState) { got = state })

	eng.onVideoStanza(videoStateNode(signaling.VideoStateStopped))

	if got.Raw != signaling.VideoStateStopped {
		t.Fatalf("video callback state = %d, want 6", got.Raw)
	}
	if active, _ := senderVideoState(m.videoTx); !active {
		t.Fatal("peer stopping video disabled the local sender")
	}
	if !call.IsSendingVideo() || call.IsReceivingVideo() || !call.IsVideo() {
		t.Fatalf("directional state after peer stop = send:%v receive:%v any:%v, want true,false,true",
			call.IsSendingVideo(), call.IsReceivingVideo(), call.IsVideo())
	}
}

func TestInboundVideoEnabledDoesNotRestartLocalFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.remoteVideo = false
	m.videoTx = &videoSender{}

	eng.onVideoStanza(videoStateNode(signaling.VideoStateEnabled))

	if active, _ := senderVideoState(m.videoTx); active {
		t.Fatal("peer enabling video restarted the local sender")
	}
	if call.IsSendingVideo() || !call.IsReceivingVideo() {
		t.Fatalf("directional state after peer enable = send:%v receive:%v, want false,true",
			call.IsSendingVideo(), call.IsReceivingVideo())
	}
}

func TestInboundVideoEnabledAcceptsPendingLocalUpgrade(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = true
	m.remoteVideo = false
	m.videoGate = true
	m.videoTx = &videoSender{active: true, sendGated: true}
	var keyframeRequests int
	call.OnVideoKeyframeRequest(func() { keyframeRequests++ })

	eng.onVideoStanza(videoStateNode(signaling.VideoStateEnabled))

	if active, gated := senderVideoState(m.videoTx); !active || gated {
		t.Fatalf("pending sender after peer enabled = active:%v gated:%v, want true,false", active, gated)
	}
	if !call.IsSendingVideo() || !call.IsReceivingVideo() {
		t.Fatalf("video flows after peer enabled = send:%v receive:%v, want true,true",
			call.IsSendingVideo(), call.IsReceivingVideo())
	}
	if keyframeRequests != 1 {
		t.Fatalf("keyframe requests = %d, want 1", keyframeRequests)
	}
}

func TestInboundVideoUpgradeWaitsForExplicitAcceptance(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.videoTx = &videoSender{}
	var sent int
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		sent++
		return nil
	}
	var got VideoState
	call.OnVideoState(func(state VideoState) { got = state })

	eng.onVideoStanza(videoStateNode(signaling.VideoStateUpgradeRequestV2))

	if !got.Upgrade || got.Raw != signaling.VideoStateUpgradeRequestV2 {
		t.Fatalf("video callback = %+v, want pending state 11", got)
	}
	if sent != 0 {
		t.Fatalf("inbound upgrade auto-sent %d signaling stanzas", sent)
	}
	if active, _ := senderVideoState(m.videoTx); active {
		t.Fatal("inbound upgrade activated video before explicit acceptance")
	}
	if !m.peerVideoUpgrade {
		t.Fatal("inbound upgrade was not marked pending")
	}
}

func TestVideoAcceptanceRequestsSourceKeyframe(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	eng.calls[call.ID()].videoTx = &videoSender{}
	eng.sendCallNode = func(context.Context, waBinary.Node) error { return nil }
	var requests int
	call.OnVideoKeyframeRequest(func() { requests++ })

	eng.onVideoStanza(videoStateNode(signaling.VideoStateUpgradeAccept))

	if requests != 1 {
		t.Fatalf("keyframe requests = %d, want 1", requests)
	}
}

func TestCallSetsVideoOrientation(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var sent waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = node
		return nil
	}

	if err := call.SetVideoOrientation(2); err != nil {
		t.Fatalf("SetVideoOrientation: %v", err)
	}
	video := sent.GetChildren()[0].AttrGetter()
	if video.Int("state") != signaling.VideoStateEnabled || video.Int("device_orientation") != 2 {
		t.Fatalf("orientation stanza attrs = %#v", sent.GetChildren()[0].Attrs)
	}
	if err := call.SetVideoOrientation(4); err == nil {
		t.Fatal("SetVideoOrientation accepted orientation 4")
	}
}

func TestDirectAudioPreacceptMatchesWorkingWireShape(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/8b80793136158e5535d5c0237e80e4d11489eb77/examples/cli/call.go#L376-L391
	ownID := types.JID{User: "111111111111111", Server: types.DefaultUserServer, Device: 9}
	wa := &whatsmeow.Client{Store: &store.Device{ID: &ownID}}
	eng := newEngine(&Client{wa: wa, log: zerolog.Nop()})
	to := peerJID()
	creator := creatorJID()
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = append(sent, node)
		return nil
	}

	if err := eng.sendPreaccept("CID", to, creator, false); err != nil {
		t.Fatalf("sendPreaccept: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent nodes = %d, want 1", len(sent))
	}
	preaccept := sent[0]
	if preaccept.Tag != "call" || preaccept.AttrGetter().JID("to") != to || preaccept.AttrGetter().OptionalString("id") == "" {
		t.Fatalf("preaccept envelope = %#v, want addressed call with generated id", preaccept)
	}
	actions := preaccept.GetChildren()
	if len(actions) != 1 || actions[0].Tag != "preaccept" {
		t.Fatalf("preaccept actions = %#v, want one preaccept", actions)
	}
	action := actions[0]
	if action.AttrGetter().String("call-id") != "CID" || action.AttrGetter().JID("call-creator") != creator {
		t.Fatalf("preaccept attrs = %#v, want call identity", action.Attrs)
	}
	children := action.GetChildren()
	if len(children) != 3 || children[0].Tag != "audio" || children[1].Tag != "encopt" || children[2].Tag != "capability" {
		t.Fatalf("preaccept children = %#v, want audio/encopt/capability", children)
	}
	if audio := children[0].AttrGetter(); audio.String("enc") != "opus" || audio.String("rate") != "16000" {
		t.Fatalf("preaccept audio attrs = %#v, want opus/16000", children[0].Attrs)
	}
	if keygen := children[1].AttrGetter().String("keygen"); keygen != "2" {
		t.Fatalf("preaccept keygen = %q, want 2", keygen)
	}
	capability := children[2]
	if len(capability.Attrs) != 1 || capability.AttrGetter().String("ver") != "1" {
		t.Fatalf("preaccept capability attrs = %#v, want only ver=1", capability.Attrs)
	}
	gotCapability, ok := capability.Content.([]byte)
	if !ok {
		t.Fatalf("preaccept capability content = %T, want []byte", capability.Content)
	}
	wantCapability := "\x01\x05\xf7\x09\xe4\xbb\x13"
	if got := string(gotCapability); got != wantCapability {
		t.Fatalf("preaccept capability = %x, want %x", []byte(got), []byte(wantCapability))
	}
}

func TestRejectFinishesOnlyAfterSignalingSucceeds(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	ownID := setTestOwnID(eng)
	wantFrom := eng.calls[call.ID()].from
	var phaseDuringSend CallPhase
	var registeredDuringSend bool
	var sent waBinary.Node
	var sendCount int
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sendCount++
		phaseDuringSend = call.State()
		registeredDuringSend = eng.lookup(call.ID()) != nil
		sent = node
		return nil
	}
	var reason string
	var endCount int
	call.OnEnd(func(got string) {
		endCount++
		reason = got
	})

	if err := call.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	if phaseDuringSend != CallPhaseRinging || !registeredDuringSend {
		t.Fatalf("state during reject send = (%d, registered:%v), want (Ringing, true)",
			phaseDuringSend, registeredDuringSend)
	}
	children := sent.GetChildren()
	if sent.Tag != "call" || len(sent.Attrs) != 3 || len(children) != 1 || children[0].Tag != "reject" {
		t.Fatalf("reject stanza shape = %#v, want one call/reject node", sent)
	}
	outer := sent.AttrGetter()
	if outer.OptionalString("id") == "" || outer.JID("from") != ownID.ToNonAD() || outer.JID("to") != wantFrom.ToNonAD() {
		t.Fatalf("reject envelope attrs = %#v, want generated id and normalized endpoints", sent.Attrs)
	}
	reject := children[0]
	rejectAttrs := reject.AttrGetter()
	if len(reject.Attrs) != 3 || rejectAttrs.String("call-id") != "CID" ||
		rejectAttrs.JID("call-creator") != wantFrom.ToNonAD() || rejectAttrs.String("count") != "0" {
		t.Fatalf("reject attrs = %#v, want call-id, normalized creator, and count=0", reject.Attrs)
	}
	if reject.Content != nil || len(reject.GetChildren()) != 0 {
		t.Fatalf("reject content = %#v, want empty content without tctoken", reject.Content)
	}
	if sendCount != 1 || call.State() != CallPhaseEnded || endCount != 1 || reason != "rejected" {
		t.Fatalf("state after reject send = (sends:%d, phase:%d, ends:%d, reason:%q), want (1, Ended, 1, rejected)",
			sendCount, call.State(), endCount, reason)
	}
	if eng.lookup(call.ID()) != nil {
		t.Fatal("Reject retained ended call in engine registry")
	}
}

func TestRejectKeepsCallWhenSignalingFails(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	setTestOwnID(eng)
	var canceled bool
	eng.calls[call.ID()].cancel = func() { canceled = true }
	sendErr := errors.New("network unavailable")
	eng.sendCallNode = func(context.Context, waBinary.Node) error { return sendErr }
	var endCount int
	call.OnEnd(func(string) { endCount++ })

	err := call.Reject()

	if !errors.Is(err, sendErr) {
		t.Fatalf("Reject error = %v, want wrapped signaling error", err)
	}
	if canceled {
		t.Fatal("Reject canceled local media after signaling failure")
	}
	if call.State() != CallPhaseRinging || endCount != 0 {
		t.Fatalf("state after failed reject = (%d, end callbacks:%d), want (Ringing, 0)",
			call.State(), endCount)
	}
	if eng.lookup(call.ID()) == nil {
		t.Fatal("Reject removed live call from engine registry after signaling failure")
	}
}

func TestRejectUnknownCallDoesNotSignal(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	setTestOwnID(eng)
	delete(eng.calls, call.ID())
	var rawRejectCount int
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		rawRejectCount++
		return nil
	}

	err := call.Reject()

	if err == nil {
		t.Fatal("Reject returned nil for an unknown call")
	}
	if rawRejectCount != 0 {
		t.Fatalf("unknown Reject raw sends = %d, want 0", rawRejectCount)
	}
	if call.State() != CallPhaseRinging {
		t.Fatalf("unknown Reject phase = %d, want Ringing", call.State())
	}
}

func TestRejectOutgoingCallDoesNotSignal(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	setTestOwnID(eng)
	var rawRejectCount int
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		rawRejectCount++
		return nil
	}

	err := call.Reject()

	if err == nil {
		t.Fatal("Reject returned nil for an outgoing call")
	}
	if rawRejectCount != 0 {
		t.Fatalf("outgoing Reject raw sends = %d, want 0", rawRejectCount)
	}
	if call.State() != CallPhaseCalling || eng.lookup(call.ID()) == nil {
		t.Fatalf("outgoing Reject state = (phase:%d, registered:%v), want (Calling, true)",
			call.State(), eng.lookup(call.ID()) != nil)
	}
}

func TestRejectNonRingingDirectCallDoesNotSignal(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	setTestOwnID(eng)
	call.setPhase(CallPhaseConnecting)
	var rawRejectCount int
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		rawRejectCount++
		return nil
	}

	err := call.Reject()

	if err == nil {
		t.Fatal("Reject returned nil for a non-ringing direct call")
	}
	if rawRejectCount != 0 {
		t.Fatalf("non-ringing Reject raw sends = %d, want 0", rawRejectCount)
	}
	if call.State() != CallPhaseConnecting || eng.lookup(call.ID()) == nil {
		t.Fatalf("non-ringing Reject state = (phase:%d, registered:%v), want (Connecting, true)",
			call.State(), eng.lookup(call.ID()) != nil)
	}
}

func TestRejectDirectCallWithoutWhatsmeowClientDoesNotSignal(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	var rawRejectCount int
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		rawRejectCount++
		return nil
	}
	var endCount int
	call.OnEnd(func(string) { endCount++ })

	err := call.Reject()

	if err == nil {
		t.Fatal("Reject returned nil without a whatsmeow client")
	}
	if rawRejectCount != 0 {
		t.Fatalf("direct Reject raw sends = %d, want 0", rawRejectCount)
	}
	if call.State() != CallPhaseRinging || endCount != 0 || eng.lookup(call.ID()) == nil {
		t.Fatalf("direct Reject state = (phase:%d, ends:%d, registered:%v), want (Ringing, 0, true)",
			call.State(), endCount, eng.lookup(call.ID()) != nil)
	}
}

func TestRejectDirectCallWithoutLoginDoesNotSignal(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	eng.c.wa = &whatsmeow.Client{Store: &store.Device{}}
	var rawRejectCount int
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		rawRejectCount++
		return nil
	}

	err := call.Reject()

	if !errors.Is(err, whatsmeow.ErrNotLoggedIn) {
		t.Fatalf("Reject error = %v, want ErrNotLoggedIn", err)
	}
	if rawRejectCount != 0 || call.State() != CallPhaseRinging || eng.lookup(call.ID()) == nil {
		t.Fatalf("logged-out Reject state = (sends:%d, phase:%d, registered:%v), want (0, Ringing, true)",
			rawRejectCount, call.State(), eng.lookup(call.ID()) != nil)
	}
}

func TestGroupRejectKeepsRawSignalingPath(t *testing.T) {
	eng, call := testEngineWithIncomingCall()
	eng.calls[call.ID()].group = true
	var rawRejectSent bool
	var reason string
	var endCount int
	call.OnEnd(func(got string) {
		endCount++
		reason = got
	})
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		rawRejectSent = true
		children := node.GetChildren()
		if node.Tag != "call" || len(children) != 1 || children[0].Tag != "reject" {
			t.Fatalf("group Reject sent %#v, want one raw reject stanza", node)
		}
		return nil
	}

	if err := call.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	if !rawRejectSent {
		t.Fatal("group Reject did not use the raw signaling path")
	}
	if call.State() != CallPhaseEnded || endCount != 1 || reason != "rejected" || eng.lookup(call.ID()) != nil {
		t.Fatalf("group Reject teardown = (phase:%d, ends:%d, reason:%q, registered:%v), want (Ended, 1, rejected, false)",
			call.State(), endCount, reason, eng.lookup(call.ID()) != nil)
	}
}

func TestHangupTearsDownLocallyWhenSignalingFails(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var canceled bool
	eng.calls[call.ID()].cancel = func() { canceled = true }
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		return errors.New("network unavailable")
	}
	var reason string
	call.OnEnd(func(got string) { reason = got })

	err := call.Hangup()

	if err == nil {
		t.Fatal("Hangup returned nil after signaling failure")
	}
	if !canceled {
		t.Fatal("Hangup did not cancel local media")
	}
	if call.State() != CallPhaseEnded || reason != "hangup" {
		t.Fatalf("local end state = (%d, %q), want (Ended, hangup)", call.State(), reason)
	}
	if eng.lookup(call.ID()) != nil {
		t.Fatal("Hangup retained ended call in engine registry")
	}
}

func TestOutgoingPeerAcceptLifecycle(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	from := types.JID{User: "222222222222222", Server: types.HiddenUserServer}

	eng.onPreAccept(&events.CallPreAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: from},
	})
	if got := call.State(); got != CallPhaseRinging {
		t.Fatalf("after preaccept phase = %d, want Ringing", got)
	}

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: from},
		Data:          &waBinary.Node{Tag: "accept"},
	})
	if got := call.State(); got != CallPhaseConnecting {
		t.Fatalf("after accept phase = %d, want Connecting", got)
	}
}

func TestOutgoingAcceptRekeysToAnsweringDevice(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.peerLID = peerJID().String()
	answeringDevice := peerJID()
	answeringDevice.Device = 7
	var rekeyed string
	m.rekeyPeer = func(peer string) error {
		rekeyed = peer
		return nil
	}

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: answeringDevice},
		Data:          &waBinary.Node{Tag: "accept"},
	})

	if rekeyed != answeringDevice.String() {
		t.Fatalf("rekeyed peer = %q, want %q", rekeyed, answeringDevice.String())
	}
	eng.mu.Lock()
	gotPeer, gotFrom := m.peerLID, m.from
	eng.mu.Unlock()
	if gotPeer != answeringDevice.String() || gotFrom != answeringDevice {
		t.Fatalf("accepted routing = (%q, %s), want (%q, %s)", gotPeer, gotFrom, answeringDevice.String(), answeringDevice)
	}
}

func TestParseRelayDataResolvesElectedPeerDevice(t *testing.T) {
	primary := peerJID()
	companion := peerJID()
	companion.Device = 7
	relay := &waBinary.Node{
		Tag:   "relay",
		Attrs: waBinary.Attrs{"peer_pid": "2", "self_pid": "1"},
		Content: []waBinary.Node{
			{Tag: "participant", Attrs: waBinary.Attrs{"pid": "0", "jid": primary}},
			{Tag: "participant", Attrs: waBinary.Attrs{"pid": "2", "jid": companion}},
		},
	}

	rd := parseRelayData(relay)

	if rd.peerJID != companion {
		t.Fatalf("relay peer = %s, want %s", rd.peerJID, companion)
	}
}

func TestRelayRekeysToElectedPeerDevice(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.peerLID = peerJID().String()
	companion := peerJID()
	companion.Device = 7
	var rekeyed string
	m.rekeyPeer = func(peer string) error {
		rekeyed = peer
		return nil
	}
	relay := &waBinary.Node{
		Tag:   "relay",
		Attrs: waBinary.Attrs{"peer_pid": "2"},
		Content: []waBinary.Node{
			{Tag: "participant", Attrs: waBinary.Attrs{"pid": "2", "jid": companion}},
		},
	}

	eng.onRelay(call.ID(), relay)

	if rekeyed != companion.String() {
		t.Fatalf("rekeyed peer = %q, want %q", rekeyed, companion.String())
	}
	eng.mu.Lock()
	gotPeer := m.peerLID
	eng.mu.Unlock()
	if gotPeer != companion.String() {
		t.Fatalf("stored peer = %q, want %q", gotPeer, companion.String())
	}
}

func TestUnqualifiedAcceptPreservesRelayElectedPeerDevice(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.peerLID = peerJID().String()
	companion := peerJID()
	companion.Device = 7
	var rekeyed []string
	m.rekeyPeer = func(peer string) error {
		rekeyed = append(rekeyed, peer)
		return nil
	}
	relay := &waBinary.Node{
		Tag:   "relay",
		Attrs: waBinary.Attrs{"peer_pid": "2"},
		Content: []waBinary.Node{
			{Tag: "participant", Attrs: waBinary.Attrs{"pid": "2", "jid": companion}},
		},
	}
	eng.onRelay(call.ID(), relay)

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: peerJID()},
		Data:          &waBinary.Node{Tag: "accept"},
	})

	if len(rekeyed) != 1 || rekeyed[0] != companion.String() {
		t.Fatalf("rekeyed peers = %v, want only %q", rekeyed, companion.String())
	}
	eng.mu.Lock()
	gotPeer := m.peerLID
	eng.mu.Unlock()
	if gotPeer != companion.String() {
		t.Fatalf("stored peer after accept = %q, want %q", gotPeer, companion.String())
	}
}

func TestOutgoingPeerAcceptCallbackFiresOnceAfterMediaStarted(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	call.setPhase(CallPhaseConnecting)
	var accepted int
	call.OnPeerAccept(func() { accepted++ })
	event := &events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: peerJID()},
		Data:          &waBinary.Node{Tag: "accept"},
	}

	eng.onAccept(event)
	eng.onAccept(event)

	if accepted != 1 {
		t.Fatalf("peer accept callbacks = %d, want 1", accepted)
	}
}

func TestOutgoingPeerAcceptCallbackReplaysAfterRegistration(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: peerJID()},
		Data:          &waBinary.Node{Tag: "accept"},
	})
	var accepted int

	call.OnPeerAccept(func() { accepted++ })

	if accepted != 1 {
		t.Fatalf("late peer accept callbacks = %d, want 1", accepted)
	}
}

func TestOutgoingPeerAcceptIgnoredAfterCallEnded(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	call.setPhase(CallPhaseEnded)
	var accepted int
	call.OnPeerAccept(func() { accepted++ })

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: peerJID()},
		Data:          &waBinary.Node{Tag: "accept"},
	})

	if accepted != 0 {
		t.Fatalf("peer accept callbacks after end = %d, want 0", accepted)
	}
}

func TestOutgoingPeerAcceptDoesNotRegressActiveCall(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	call.setPhase(CallPhaseActive)

	eng.onPreAccept(&events.CallPreAccept{BasicCallMeta: types.BasicCallMeta{CallID: "CID"}})
	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID"},
		Data:          &waBinary.Node{Tag: "accept"},
	})
	if got := call.State(); got != CallPhaseActive {
		t.Fatalf("phase = %d, want Active", got)
	}
}

func TestPeerRejectEndsCall(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var reason string
	call.OnEnd(func(r string) { reason = r })

	eng.onReject(&events.CallReject{BasicCallMeta: types.BasicCallMeta{CallID: "CID"}})
	if got := call.State(); got != CallPhaseEnded {
		t.Fatalf("phase = %d, want Ended", got)
	}
	if reason != "rejected" {
		t.Fatalf("reason = %q, want rejected", reason)
	}
}

func TestFinishCallClosesAttachedAudioDevices(t *testing.T) {
	source := &lifecycleAudioSource{}
	sink := &lifecycleAudioSink{}
	call := &Call{id: "CALL", phase: CallPhaseActive}
	call.Play(source)
	call.Receive(sink)
	eng := &engine{calls: map[string]*engineCall{
		"CALL": {call: call},
	}}

	eng.finishCall("CALL", "done")

	if source.closeCount != 1 {
		t.Fatalf("audio source close count = %d, want 1", source.closeCount)
	}
	if sink.closeCount != 1 {
		t.Fatalf("audio sink close count = %d, want 1", sink.closeCount)
	}
}
