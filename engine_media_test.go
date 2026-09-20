package meowcaller

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/diag"
	"github.com/purpshell/meowcaller/rtp"
)

type recordingVideoSink struct {
	legacyCalls    int
	timestampCalls int
	accessUnit     []byte
	timestamp      uint32
	ssrc           uint32
	err            error
}

func (s *recordingVideoSink) WriteVideo(accessUnit []byte) error {
	s.legacyCalls++
	s.accessUnit = bytes.Clone(accessUnit)
	return s.err
}

func (s *recordingVideoSink) WriteVideoWithTimestamp(accessUnit []byte, timestamp uint32, ssrc uint32) error {
	s.timestampCalls++
	s.accessUnit = bytes.Clone(accessUnit)
	s.timestamp = timestamp
	s.ssrc = ssrc
	return s.err
}

func (s *recordingVideoSink) Close() error { return nil }

type legacyRecordingVideoSink struct {
	calls      int
	accessUnit []byte
	err        error
}

func (s *legacyRecordingVideoSink) WriteVideo(accessUnit []byte) error {
	s.calls++
	s.accessUnit = bytes.Clone(accessUnit)
	return s.err
}

func (s *legacyRecordingVideoSink) Close() error { return nil }

func TestWriteVideoFrameUsesTimestampSinkOnceWithOriginalMetadata(t *testing.T) {
	accessUnit := []byte{0, 0, 0, 1, 0x65, 1, 2, 3}
	tests := []struct {
		name      string
		timestamp uint32
		ssrc      uint32
	}{
		{name: "zero", timestamp: 0, ssrc: 0x10203040},
		{name: "non-zero", timestamp: 90_000, ssrc: 0x50607080},
		{name: "wrap boundary", timestamp: ^uint32(0), ssrc: 0x90a0b0c0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &recordingVideoSink{}
			if err := writeVideoFrame(sink, accessUnit, tt.timestamp, tt.ssrc); err != nil {
				t.Fatalf("write video frame: %v", err)
			}
			if sink.timestampCalls != 1 || sink.legacyCalls != 0 {
				t.Fatalf("calls = timestamp %d, legacy %d; want 1, 0", sink.timestampCalls, sink.legacyCalls)
			}
			if !bytes.Equal(sink.accessUnit, accessUnit) || sink.timestamp != tt.timestamp || sink.ssrc != tt.ssrc {
				t.Fatalf("frame = (%x, %d, %#x), want (%x, %d, %#x)", sink.accessUnit, sink.timestamp, sink.ssrc, accessUnit, tt.timestamp, tt.ssrc)
			}
		})
	}
}

func TestWriteVideoFrameFallsBackToLegacySink(t *testing.T) {
	accessUnit := []byte{0, 0, 0, 1, 0x41, 4, 5, 6}
	sink := &legacyRecordingVideoSink{}
	if err := writeVideoFrame(sink, accessUnit, 90_000, 0x10203040); err != nil {
		t.Fatalf("write video frame: %v", err)
	}
	if sink.calls != 1 || !bytes.Equal(sink.accessUnit, accessUnit) {
		t.Fatalf("legacy delivery = (%d, %x), want (1, %x)", sink.calls, sink.accessUnit, accessUnit)
	}
}

func TestWriteVideoFramePropagatesSinkErrors(t *testing.T) {
	wantErr := errors.New("sink failed")
	tests := []struct {
		name string
		sink VideoSink
	}{
		{name: "timestamp sink", sink: &recordingVideoSink{err: wantErr}},
		{name: "legacy sink", sink: &legacyRecordingVideoSink{err: wantErr}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := writeVideoFrame(tt.sink, []byte{0, 0, 0, 1, 0x65}, 123, 456); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestVideoRtpDurationSamples(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     uint32
	}{
		{name: "zero uses fallback", duration: 0, want: defaultVideoRtpStepSamples},
		{name: "negative uses fallback", duration: -time.Millisecond, want: defaultVideoRtpStepSamples},
		{name: "30 fps", duration: time.Second / 30, want: 3000},
		{name: "60 fps", duration: time.Second / 60, want: 1500},
		{name: "sub sample uses fallback", duration: time.Nanosecond, want: defaultVideoRtpStepSamples},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := videoRtpDurationSamples(tt.duration); got != tt.want {
				t.Fatalf("videoRtpDurationSamples(%s) = %d, want %d", tt.duration, got, tt.want)
			}
		})
	}
}

func TestVideoPLIThrottleIsParticipantScoped(t *testing.T) {
	lastSent := make(map[uint32]time.Time)
	now := time.Unix(100, 0)
	if !shouldSendVideoPLI(lastSent, 1, now) {
		t.Fatal("first PLI was throttled")
	}
	if shouldSendVideoPLI(lastSent, 1, now.Add(videoPLIInterval-time.Millisecond)) {
		t.Fatal("repeated PLI inside interval was sent")
	}
	if !shouldSendVideoPLI(lastSent, 2, now.Add(time.Millisecond)) {
		t.Fatal("another participant's first PLI was throttled")
	}
	if !shouldSendVideoPLI(lastSent, 1, now.Add(videoPLIInterval)) {
		t.Fatal("PLI at interval boundary remained throttled")
	}
}

func TestVideoSenderStartsAtIDRAndUsesWhatsappHeaders(t *testing.T) {
	callKey := iota32()
	pipe, err := NewMediaPipeline(callKey, "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{
		pipe:             pipe,
		stream:           rtp.NewVideoRtpStream(0x55667788, 4500),
		active:           true,
		keyframeRequired: true,
	}

	delta := []byte{0, 0, 0, 1, 0x41, 1, 2, 3}
	if packets := sender.protectAccessUnit(delta, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("dependent frame produced %d packets before an IDR", len(packets))
	}

	idr := []byte{
		0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
		0, 0, 0, 1, 0x68, 0xce, 6, 0xe2,
		0, 0, 0, 1, 0x65, 1, 2, 3,
	}
	packets := sender.protectAccessUnit(idr, 50*time.Millisecond)
	if len(packets) != 1 {
		t.Fatalf("IDR produced %d packets, want one packed access-unit packet", len(packets))
	}
	receiver, err := NewMediaPipeline(callKey, "222222222222222:0@lid", "111111111111111:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("receiver pipe: %v", err)
	}
	var depack rtp.H264Depacketizer
	var reconstructed []byte
	for i, packet := range packets {
		header, ok := rtp.ParseRtpHeader(packet)
		if !ok {
			t.Fatalf("packet %d has no RTP header", i)
		}
		wantHeaderSize := rtp.WhatsappVideoRtpHeaderSize
		if i == 0 {
			wantHeaderSize += 4
		}
		if n, ok := rtp.RtpHeaderByteLength(packet); !ok || n != wantHeaderSize {
			t.Fatalf("packet %d header length = (%d, %v), want (%d, true)", i, n, ok, wantHeaderSize)
		}
		if header.VideoExtension == nil || header.VideoExtension.MediaFrameInfo != rtp.VideoMediaFrameInfoIDR {
			t.Fatalf("packet %d video extension = %+v", i, header.VideoExtension)
		}
		_, payload, ok := receiver.UnprotectAudio(packet)
		if !ok {
			t.Fatalf("packet %d did not unprotect", i)
		}
		for _, nalu := range depack.Depacketize(payload) {
			reconstructed = append(reconstructed, 0, 0, 0, 1)
			reconstructed = append(reconstructed, nalu...)
		}
	}
	if !bytes.Equal(reconstructed, idr) {
		t.Fatalf("reconstructed access unit = %x, want %x", reconstructed, idr)
	}
}

func TestVideoSenderRecordsWireDiagnostics(t *testing.T) {
	dir := t.TempDir()
	rec, err := diag.NewRecorder(dir)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	pipe, err := NewMediaPipeline(iota32(), "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{
		pipe: pipe, stream: rtp.NewVideoRtpStream(0x55667788, 4500),
		callID: "test-call", active: true, keyframeRequired: true, diag: rec,
	}
	idr := []byte{0, 0, 0, 1, 0x65, 1, 2, 3}
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 1 {
		t.Fatalf("IDR produced %d packets, want 1", len(packets))
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "video_wire.jsonl"))
	if err != nil {
		t.Fatalf("read video wire diagnostics: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"event":"access_unit"`, `"event":"packet"`, `"direction":"out"`, `"call_id":"test-call"`, `"header_hex":`, `"payload_bytes":`, `"protected_bytes":`} {
		if !strings.Contains(text, want) {
			t.Errorf("video wire diagnostics missing %s: %s", want, text)
		}
	}
	for _, forbidden := range []string{`"payload_hex":`, `"protected_hex":`, `"annexb_hex":`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("video wire diagnostics exposed sensitive media in %s", text)
		}
	}
}

func TestVideoSenderGatesUpgradeUntilPeerAcceptance(t *testing.T) {
	pipe, err := NewMediaPipeline(iota32(), "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{pipe: pipe, stream: rtp.NewVideoRtpStream(0x55667788, 4500)}
	idr := []byte{0, 0, 0, 1, 0x65, 1, 2, 3}
	delta := []byte{0, 0, 0, 1, 0x41, 1, 2, 3}

	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("inactive sender produced %d packets", len(packets))
	}
	sender.enable(true)
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("send-gated sender produced %d packets", len(packets))
	}
	sender.enable(false)
	if packets := sender.protectAccessUnit(delta, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("ungated sender sent dependent frame before recovery IDR: %d packets", len(packets))
	}
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) == 0 {
		t.Fatal("ungated sender did not send recovery IDR")
	}
	sender.disable()
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("disabled sender produced %d packets", len(packets))
	}
}

func TestMediaSrtcpSenderProtectsVideoReport(t *testing.T) {
	sender, err := newMediaSrtcpSender(iota32(), "111111111111111:0@lid", 0x55667788, true)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	packet, err := sender.senderReport(rtp.RtcpSenderStats{
		PacketsSent:  3,
		OctetsSent:   400,
		RtpTimestamp: 90000,
	}, 1700000000000, nil)
	if err != nil {
		t.Fatalf("sender report: %v", err)
	}
	if kind := rtp.IsRtcpPacket(packet); !kind {
		t.Fatal("protected sender report is not classified as RTCP")
	}
	if len(packet) != 60+14 {
		t.Fatalf("protected report length = %d, want 74", len(packet))
	}
}

func TestMediaSrtcpReceiverRekeysForAnsweringDevice(t *testing.T) {
	callKey := iota32()
	const ssrc = 0x55667788
	receiver, err := newMediaSrtcpReceiver(callKey, "222222222222222:0@lid")
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	sender, err := newMediaSrtcpSender(callKey, "222222222222222:7@lid", ssrc, true)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	packet, err := sender.senderReport(rtp.RtcpSenderStats{PacketsSent: 1}, 1700000000000, nil)
	if err != nil {
		t.Fatalf("sender report: %v", err)
	}

	if err = receiver.rekey(callKey, "222222222222222:7@lid"); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if _, _, ok := receiver.unprotect(ssrc, packet); !ok {
		t.Fatal("rekeyed SRTCP receiver rejected answering-device report")
	}
}
