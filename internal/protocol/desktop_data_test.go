package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDesktopPointerRequiresNormalizedCoordinatesAndCurrentDisplay(t *testing.T) {
	event := DesktopPointerEvent{DisplayID: "display-1", Generation: 3, Kind: "move", X: 0.5, Y: 0.25, Button: -1}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	event.X = 1.01
	if err := event.Validate(); err == nil {
		t.Fatal("out-of-range coordinate accepted")
	}
	event.X, event.WheelY = 0.5, DesktopMaxWheelDelta
	if err := event.Validate(); err != nil {
		t.Fatalf("maximum wheel delta rejected: %v", err)
	}
	event.WheelY = DesktopMaxWheelDelta + 1
	if err := event.Validate(); err == nil {
		t.Fatal("unsafe wheel delta accepted")
	}
	event.WheelY = math.Inf(1)
	if err := event.Validate(); err == nil {
		t.Fatal("non-finite wheel delta accepted")
	}
	event.X, event.Generation = 0.5, 0
	if err := event.Validate(); err == nil {
		t.Fatal("missing generation accepted")
	}
}

func TestDesktopKeyboardPhysicalFieldsAndBounds(t *testing.T) {
	event := DesktopKeyboardEvent{Code: "ShiftLeft", ScanCode: 42, Location: 1, Down: true, Shift: true}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	event.Location = 4
	if err := event.Validate(); err == nil {
		t.Fatal("invalid keyboard location accepted")
	}
}

func TestDesktopClipboardAndSpecialKeyBounds(t *testing.T) {
	if err := (DesktopClipboardText{Direction: "browser_to_agent", Text: strings.Repeat("x", (1<<20)+1)}).Validate(); err == nil {
		t.Fatal("oversized clipboard accepted")
	}
	if err := (DesktopClipboardText{Direction: "agent_to_browser", Text: "ok"}).Validate(); err != nil {
		t.Fatalf("clipboard rejected: %v", err)
	}
	if err := (DesktopSpecialKey{Name: "arbitrary_command"}).Validate(); err == nil {
		t.Fatal("unknown special key accepted")
	}
	if err := (DesktopSpecialKey{Name: "alt_tab"}).Validate(); err != nil {
		t.Fatalf("special key rejected: %v", err)
	}
}

func TestDesktopQualityLevelsAndStatisticsAreBoundedAndContentFree(t *testing.T) {
	want := []DesktopQualityLevel{
		{Name: "low", Scale: .5, FPS: 15, BitrateBPS: 1_000_000},
		{Name: "balanced", Scale: .75, FPS: 30, BitrateBPS: 4_000_000},
		{Name: "high", Scale: 1, FPS: 30, BitrateBPS: 8_000_000},
		{Name: "ultra", Scale: 1, FPS: 60, BitrateBPS: 20_000_000},
	}
	if !reflect.DeepEqual(DesktopQualityLevels, want) {
		t.Fatalf("levels = %#v", DesktopQualityLevels)
	}
	statistics := DesktopStatistics{RTTMS: 120, DecoderQueue: 3, DecodedFrames: 300, DroppedFrames: 2, SenderQueueBytes: 1024,
		RelayBackpressureCount: 1, EncodedBitrateBPS: 4_000_000, AppliedWidth: 1440, AppliedHeight: 900}
	if err := statistics.Validate(); err != nil {
		t.Fatalf("statistics rejected: %v", err)
	}
	statistics.SenderQueueBytes = DesktopMaxQualityQueueBytes + 1
	if !errors.Is(statistics.Validate(), ErrDesktopQualityInvalid) {
		t.Fatalf("oversized queue accepted: %v", statistics.Validate())
	}
}

func TestDesktopControlAndDisplaySelectionRequireCurrentLeaseAndGeneration(t *testing.T) {
	if err := (DesktopControlMode{Enabled: true, FocusLease: 9}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (DesktopControlMode{Enabled: true}).Validate(); err == nil {
		t.Fatal("enabled control without lease accepted")
	}
	if err := (DesktopDisplaySelection{DisplayID: "display-2", Generation: 7}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (DesktopDisplaySelection{DisplayID: "display-2"}).Validate(); err == nil {
		t.Fatal("display without generation accepted")
	}
}

func TestDesktopInnerMessageTypeCompatibility(t *testing.T) {
	want := map[uint16]string{
		1: "codec_config", 2: "video_access_unit", 3: "pointer_shape", 4: "display_inventory",
		10: "keyboard", 11: "pointer", 12: "clipboard_offer", 13: "clipboard_text",
		20: "control_mode", 21: "quality", 30: "ping", 31: "pong", 32: "statistics",
		40: "secure_state", 41: "permission_state", 255: "terminate",
	}
	for id, name := range want {
		if got := DesktopInnerMessageNames[id]; got != name {
			t.Fatalf("type %d = %q, want %q", id, got, name)
		}
	}
}

func TestDesktopDisplayDescriptorRequiresStableIDAndPositiveGeometry(t *testing.T) {
	valid := DesktopDisplayDescriptor{ID: "display-1", Name: "Primary", X: 0, Y: 0, Width: 1920, Height: 1080, Scale: 1, Primary: true}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	valid.Width = 0
	if err := valid.Validate(); err == nil {
		t.Fatal("zero-width display accepted")
	}
}

func TestDesktopStreamGenerationMustIncreaseOnGeometryChange(t *testing.T) {
	state := DesktopStreamState{Generation: 4, DisplayID: "display-1", Width: 1920, Height: 1080}
	if err := state.ApplyConfig(DesktopCodecConfig{Generation: 4, DisplayID: "display-1", Width: 1280, Height: 720}); err == nil {
		t.Fatal("geometry changed without generation increment")
	}
	if err := state.ApplyConfig(DesktopCodecConfig{Generation: 5, DisplayID: "display-1", Width: 1280, Height: 720}); err != nil {
		t.Fatalf("incremented generation rejected: %v", err)
	}
}

func TestDesktopRequiredUnknownMessageFailsClosed(t *testing.T) {
	_, err := DecodeDesktopInnerMessage([]byte{1, 0, 0x7f, 0xff, 0, 0, 0, 0})
	if !errors.Is(err, ErrDesktopRequiredMessageUnknown) {
		t.Fatalf("Decode = %v", err)
	}
}

func TestDesktopDataPlaneFrameKindsAreStable(t *testing.T) {
	if DesktopFrameBrowserHandshake != 1 || DesktopFrameAgentHandshake != 2 || DesktopFrameEncryptedRecord != 3 {
		t.Fatal("data-plane frame ids changed")
	}
}

func TestDesktopHandshakeJSONFieldsAreStable(t *testing.T) {
	encoded, err := EncodeDesktopHandshake(DesktopBrowserHandshake{TranscriptBase64URL: "transcript", BrowserEphemeralPublicKeyBase64URL: "ephemeral", OperatorSignatureBase64URL: "signature"})
	if err != nil || string(encoded) != `{"transcript_base64url":"transcript","browser_ephemeral_public_key_base64url":"ephemeral","operator_signature_base64url":"signature"}` {
		t.Fatalf("browser handshake = %s, %v", encoded, err)
	}
	encoded, err = EncodeDesktopHandshake(DesktopAgentHandshake{EndpointEphemeralPublicKeyBase64URL: "ephemeral", EndpointHandshakeSignatureBase64URL: "signature"})
	if err != nil || string(encoded) != `{"endpoint_ephemeral_public_key_base64url":"ephemeral","endpoint_handshake_signature_base64url":"signature"}` {
		t.Fatalf("agent handshake = %s, %v", encoded, err)
	}
}

func TestDesktopDataPlaneAndInnerRoundTrip(t *testing.T) {
	message, err := EncodeDesktopInnerMessage(DesktopMessageKeyboard, 0, []byte(`{"code":"KeyA"}`))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDesktopInnerMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Header.Type != DesktopMessageKeyboard || string(decoded.Payload) != `{"code":"KeyA"}` {
		t.Fatalf("decoded = %#v", decoded)
	}
	frame, err := EncodeDesktopDataFrame(DesktopFrameEncryptedRecord, message)
	if err != nil {
		t.Fatal(err)
	}
	decodedFrame, err := DecodeDesktopDataFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if decodedFrame.Kind != DesktopFrameEncryptedRecord || string(decodedFrame.Payload) != string(message) {
		t.Fatalf("frame = %#v", decodedFrame)
	}
}

func TestDesktopClipboardInnerAllowsOneMiBTextJSONButKeepsGenericControlBounded(t *testing.T) {
	if _, err := EncodeDesktopInnerMessage(DesktopMessageQuality, 0, make([]byte, DesktopMaxControlPayload)); err != nil {
		t.Fatalf("control boundary: %v", err)
	}
	if _, err := EncodeDesktopInnerMessage(DesktopMessageQuality, 0, make([]byte, DesktopMaxControlPayload+1)); !errors.Is(err, ErrDesktopInnerBounds) {
		t.Fatalf("oversized control = %v", err)
	}
	payload, err := json.Marshal(DesktopClipboardText{Direction: "browser_to_agent", Text: strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= DesktopMaxControlPayload {
		t.Fatal("clipboard fixture did not cross generic control bound")
	}
	inner, err := EncodeDesktopInnerMessage(DesktopMessageClipboardText, 0, payload)
	if err != nil {
		t.Fatalf("clipboard boundary: %v", err)
	}
	if _, err := DecodeDesktopInnerMessage(inner); err != nil {
		t.Fatalf("clipboard decode: %v", err)
	}
}

func TestDesktopInnerBoundsAndOptionalUnknown(t *testing.T) {
	optional, err := EncodeDesktopInnerMessage(0x7fff, DesktopInnerOptional, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDesktopInnerMessage(optional)
	if err != nil || !decoded.UnknownOptional {
		t.Fatalf("optional unknown = %#v, %v", decoded, err)
	}
	if _, err := EncodeDesktopInnerMessage(DesktopMessageKeyboard, 0, make([]byte, DesktopMaxControlPayload+1)); !errors.Is(err, ErrDesktopInnerBounds) {
		t.Fatalf("oversized control = %v", err)
	}
	if _, err := DecodeDesktopDataFrame([]byte("wrong header")); err == nil {
		t.Fatal("bad pre-key frame accepted")
	}
}

func TestDesktopDataPlaneGoldenVectors(t *testing.T) {
	path := filepath.Join("..", "..", "schemas", "desktop", "v1", "test-vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		DataPlane struct {
			Browser string            `json:"browser_handshake_frame_base64url"`
			Agent   string            `json:"agent_handshake_frame_base64url"`
			Inner   map[string]string `json:"inner_messages"`
			Invalid []struct {
				Name string `json:"name"`
			} `json:"invalid_epoch_records"`
		} `json:"data_plane"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	decode := func(value string) []byte {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	browser, err := DecodeDesktopDataFrame(decode(fixture.DataPlane.Browser))
	if err != nil || browser.Kind != DesktopFrameBrowserHandshake {
		t.Fatalf("browser vector = %#v, %v", browser, err)
	}
	agent, err := DecodeDesktopDataFrame(decode(fixture.DataPlane.Agent))
	if err != nil || agent.Kind != DesktopFrameAgentHandshake {
		t.Fatalf("agent vector = %#v, %v", agent, err)
	}
	for name, value := range fixture.DataPlane.Inner {
		_, err := DecodeDesktopInnerMessage(decode(value))
		wantError := name == "unknown_required_base64url" || name == "oversized_control_header_base64url"
		if (err != nil) != wantError {
			t.Fatalf("%s error = %v, wantError %v", name, err, wantError)
		}
	}
	if len(fixture.DataPlane.Invalid) != 3 {
		t.Fatalf("invalid epoch vectors = %d", len(fixture.DataPlane.Invalid))
	}
}

func TestDesktopNativeControlGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "desktop", "v1", "test-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Control map[string]json.RawMessage `json:"control_semantics"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	decode := func(name string) []byte {
		var value string
		if err := json.Unmarshal(fixture.Control[name], &value); err != nil {
			t.Fatal(err)
		}
		data, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var pointer DesktopPointerEvent
	if err := json.Unmarshal(decode("pointer_edges_base64url"), &pointer); err != nil || pointer.Validate() != nil || pointer.Generation != ^uint32(0) {
		t.Fatalf("pointer = %#v, %v", pointer, err)
	}
	var wheel DesktopPointerEvent
	if err := json.Unmarshal(decode("wheel_bound_base64url"), &wheel); err != nil || wheel.Validate() != nil || wheel.WheelY != DesktopMaxWheelDelta {
		t.Fatalf("canonical wheel boundary invalid: %+v %v", wheel, err)
	}
	if err := json.Unmarshal(decode("wheel_overflow_base64url"), &wheel); err != nil || wheel.Validate() == nil {
		t.Fatalf("canonical wheel overflow accepted: %+v %v", wheel, err)
	}
	var keyboard DesktopKeyboardEvent
	if err := json.Unmarshal(decode("keyboard_modifiers_base64url"), &keyboard); err != nil || keyboard.Validate() != nil || !keyboard.Repeat || keyboard.Location != 2 {
		t.Fatalf("keyboard = %#v, %v", keyboard, err)
	}
	var special DesktopSpecialKey
	_ = json.Unmarshal(decode("special_key_base64url"), &special)
	if special.Validate() != nil {
		t.Fatal("canonical special key rejected")
	}
	_ = json.Unmarshal(decode("unknown_special_key_base64url"), &special)
	if special.Validate() == nil {
		t.Fatal("unknown canonical special key accepted")
	}
}
