package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

type DesktopBrowserHandshake struct {
	TranscriptBase64URL                string `json:"transcript_base64url"`
	BrowserEphemeralPublicKeyBase64URL string `json:"browser_ephemeral_public_key_base64url"`
	OperatorSignatureBase64URL         string `json:"operator_signature_base64url"`
}

type DesktopAgentHandshake struct {
	EndpointEphemeralPublicKeyBase64URL string `json:"endpoint_ephemeral_public_key_base64url"`
	EndpointHandshakeSignatureBase64URL string `json:"endpoint_handshake_signature_base64url"`
}

func EncodeDesktopHandshake(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > DesktopMaxControlPayload {
		return nil, ErrDesktopDataFrameInvalid
	}
	return encoded, nil
}

const (
	DesktopFrameBrowserHandshake byte = 1
	DesktopFrameAgentHandshake   byte = 2
	DesktopFrameEncryptedRecord  byte = 3

	DesktopInnerVersion  byte = 1
	DesktopInnerOptional byte = 1

	DesktopMaxControlPayload = 256 << 10
	DesktopMaxVideoPayload   = 4 << 20
	// A 1 MiB UTF-8 clipboard value may expand to six JSON escape bytes per
	// input byte. The fixed allowance keeps that expansion bounded.
	DesktopMaxClipboardWirePayload  = 6*(1<<20) + 128
	DesktopMaxEncryptedFramePayload = DesktopMaxClipboardWirePayload + 64
	DesktopMaxWheelDelta            = 1_000_000.0
	DesktopMaxQualityQueueBytes     = 64 << 20
)

const (
	DesktopMessageCodecConfig      uint16 = 1
	DesktopMessageVideoAccessUnit  uint16 = 2
	DesktopMessagePointerShape     uint16 = 3
	DesktopMessageDisplayInventory uint16 = 4
	DesktopMessageKeyboard         uint16 = 10
	DesktopMessagePointer          uint16 = 11
	DesktopMessageClipboardOffer   uint16 = 12
	DesktopMessageClipboardText    uint16 = 13
	DesktopMessageDisplaySelection uint16 = 14
	DesktopMessageSpecialKey       uint16 = 15
	DesktopMessageControlMode      uint16 = 20
	DesktopMessageQuality          uint16 = 21
	DesktopMessagePing             uint16 = 30
	DesktopMessagePong             uint16 = 31
	DesktopMessageStatistics       uint16 = 32
	DesktopMessageSecureState      uint16 = 40
	DesktopMessagePermissionState  uint16 = 41
	DesktopMessageTerminate        uint16 = 255
)

var DesktopInnerMessageNames = map[uint16]string{
	DesktopMessageCodecConfig: "codec_config", DesktopMessageVideoAccessUnit: "video_access_unit",
	DesktopMessagePointerShape: "pointer_shape", DesktopMessageDisplayInventory: "display_inventory",
	DesktopMessageKeyboard: "keyboard", DesktopMessagePointer: "pointer",
	DesktopMessageClipboardOffer: "clipboard_offer", DesktopMessageClipboardText: "clipboard_text",
	DesktopMessageDisplaySelection: "display_selection", DesktopMessageSpecialKey: "special_key",
	DesktopMessageControlMode: "control_mode", DesktopMessageQuality: "quality",
	DesktopMessagePing: "ping", DesktopMessagePong: "pong", DesktopMessageStatistics: "statistics",
	DesktopMessageSecureState: "secure_state", DesktopMessagePermissionState: "permission_state",
	DesktopMessageTerminate: "terminate",
}

var (
	ErrDesktopDataFrameInvalid        = errors.New("desktop_data_frame_invalid")
	ErrDesktopInnerBounds             = errors.New("desktop_inner_bounds")
	ErrDesktopInnerInvalid            = errors.New("desktop_inner_invalid")
	ErrDesktopRequiredMessageUnknown  = errors.New("desktop_required_message_unknown")
	ErrDesktopDisplayInvalid          = errors.New("desktop_display_invalid")
	ErrDesktopStreamGenerationInvalid = errors.New("desktop_stream_generation_invalid")
	ErrDesktopPointerInvalid          = errors.New("desktop_pointer_invalid")
	ErrDesktopKeyboardInvalid         = errors.New("desktop_keyboard_invalid")
	ErrDesktopClipboardInvalid        = errors.New("desktop_clipboard_invalid")
	ErrDesktopControlModeInvalid      = errors.New("desktop_control_mode_invalid")
	ErrDesktopDisplaySelectionInvalid = errors.New("desktop_display_selection_invalid")
	ErrDesktopSpecialKeyInvalid       = errors.New("desktop_special_key_invalid")
	ErrDesktopQualityInvalid          = errors.New("desktop_quality_invalid")
)

var desktopDataMagic = [4]byte{'H', 'D', 'V', '1'}

type DesktopDataFrameHeader struct {
	Kind          byte
	PayloadLength uint32
}

type DesktopDataFrame struct {
	Kind    byte
	Payload []byte
}

func (h DesktopDataFrameHeader) MarshalBinary() ([]byte, error) {
	if h.Kind < DesktopFrameBrowserHandshake || h.Kind > DesktopFrameEncryptedRecord || h.PayloadLength > DesktopMaxEncryptedFramePayload {
		return nil, ErrDesktopDataFrameInvalid
	}
	out := make([]byte, 12)
	copy(out[:4], desktopDataMagic[:])
	out[4] = h.Kind
	binary.BigEndian.PutUint32(out[8:12], h.PayloadLength)
	return out, nil
}

func EncodeDesktopDataFrame(kind byte, payload []byte) ([]byte, error) {
	header, err := (DesktopDataFrameHeader{Kind: kind, PayloadLength: uint32(len(payload))}).MarshalBinary()
	if err != nil {
		return nil, err
	}
	return append(header, payload...), nil
}

func DecodeDesktopDataFrame(frame []byte) (DesktopDataFrame, error) {
	if len(frame) < 12 || string(frame[:4]) != string(desktopDataMagic[:]) || frame[5] != 0 || frame[6] != 0 || frame[7] != 0 {
		return DesktopDataFrame{}, ErrDesktopDataFrameInvalid
	}
	header := DesktopDataFrameHeader{Kind: frame[4], PayloadLength: binary.BigEndian.Uint32(frame[8:12])}
	if _, err := header.MarshalBinary(); err != nil || uint64(header.PayloadLength)+12 != uint64(len(frame)) {
		return DesktopDataFrame{}, ErrDesktopDataFrameInvalid
	}
	return DesktopDataFrame{Kind: header.Kind, Payload: append([]byte(nil), frame[12:]...)}, nil
}

type DesktopInnerHeader struct {
	Version       byte
	Flags         byte
	Type          uint16
	PayloadLength uint32
}

type DesktopInnerMessage struct {
	Header          DesktopInnerHeader
	Payload         []byte
	UnknownOptional bool
}

func (h DesktopInnerHeader) MarshalBinary() ([]byte, error) {
	if h.Version != DesktopInnerVersion || h.Flags & ^DesktopInnerOptional != 0 {
		return nil, ErrDesktopInnerInvalid
	}
	limit := uint32(DesktopMaxControlPayload)
	if h.Type == DesktopMessageVideoAccessUnit {
		limit = DesktopMaxVideoPayload
	} else if h.Type == DesktopMessageClipboardText {
		limit = DesktopMaxClipboardWirePayload
	}
	if h.PayloadLength > limit {
		return nil, ErrDesktopInnerBounds
	}
	out := make([]byte, 8)
	out[0], out[1] = h.Version, h.Flags
	binary.BigEndian.PutUint16(out[2:4], h.Type)
	binary.BigEndian.PutUint32(out[4:8], h.PayloadLength)
	return out, nil
}

func EncodeDesktopInnerMessage(messageType uint16, flags byte, payload []byte) ([]byte, error) {
	header, err := (DesktopInnerHeader{Version: DesktopInnerVersion, Flags: flags, Type: messageType, PayloadLength: uint32(len(payload))}).MarshalBinary()
	if err != nil {
		return nil, err
	}
	if _, known := DesktopInnerMessageNames[messageType]; !known && flags&DesktopInnerOptional == 0 {
		return nil, ErrDesktopRequiredMessageUnknown
	}
	return append(header, payload...), nil
}

func DecodeDesktopInnerMessage(encoded []byte) (DesktopInnerMessage, error) {
	if len(encoded) < 8 {
		return DesktopInnerMessage{}, ErrDesktopInnerInvalid
	}
	header := DesktopInnerHeader{Version: encoded[0], Flags: encoded[1], Type: binary.BigEndian.Uint16(encoded[2:4]), PayloadLength: binary.BigEndian.Uint32(encoded[4:8])}
	if _, err := header.MarshalBinary(); err != nil {
		return DesktopInnerMessage{}, err
	}
	if uint64(header.PayloadLength)+8 != uint64(len(encoded)) {
		return DesktopInnerMessage{}, ErrDesktopInnerBounds
	}
	_, known := DesktopInnerMessageNames[header.Type]
	if !known && header.Flags&DesktopInnerOptional == 0 {
		return DesktopInnerMessage{}, ErrDesktopRequiredMessageUnknown
	}
	return DesktopInnerMessage{Header: header, Payload: append([]byte(nil), encoded[8:]...), UnknownOptional: !known}, nil
}

// Milestone 2 control payloads are UTF-8 JSON inside the encrypted record.
type DesktopCodecConfig struct {
	Codec       string `json:"codec"`
	Generation  uint64 `json:"generation"`
	DisplayID   string `json:"display_id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Description string `json:"description_base64url"`
}
type DesktopDisplayInventory struct {
	Displays []DesktopDisplayDescriptor `json:"displays"`
}
type DesktopDisplayDescriptor struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	X        int     `json:"x"`
	Y        int     `json:"y"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Scale    float64 `json:"scale"`
	Primary  bool    `json:"primary"`
	Rotation int     `json:"rotation"`
}

func (display DesktopDisplayDescriptor) Validate() error {
	if display.ID == "" || display.Name == "" || display.Width <= 0 || display.Height <= 0 || display.Scale <= 0 ||
		(display.Rotation != 0 && display.Rotation != 90 && display.Rotation != 180 && display.Rotation != 270) {
		return ErrDesktopDisplayInvalid
	}
	return nil
}

// DesktopDisplay is retained as a source-compatible alias for Milestone 2 callers.
type DesktopDisplay = DesktopDisplayDescriptor

type DesktopStreamState struct {
	Generation uint64
	DisplayID  string
	Codec      string
	Width      int
	Height     int
}

func (state *DesktopStreamState) ApplyConfig(config DesktopCodecConfig) error {
	if config.Generation == 0 || config.DisplayID == "" || config.Width <= 0 || config.Height <= 0 {
		return ErrDesktopStreamGenerationInvalid
	}
	if state.Generation > 0 {
		changed := state.DisplayID != config.DisplayID || state.Codec != config.Codec || state.Width != config.Width || state.Height != config.Height
		if config.Generation < state.Generation || (config.Generation == state.Generation && changed) {
			return ErrDesktopStreamGenerationInvalid
		}
	}
	state.Generation, state.DisplayID, state.Codec, state.Width, state.Height = config.Generation, config.DisplayID, config.Codec, config.Width, config.Height
	return nil
}

type DesktopKeyboardEvent struct {
	Code        string `json:"code"`
	ScanCode    uint32 `json:"scan_code"`
	Location    uint8  `json:"location"`
	Down        bool   `json:"down"`
	Repeat      bool   `json:"repeat"`
	Shift       bool   `json:"shift"`
	Control     bool   `json:"control"`
	Alt         bool   `json:"alt"`
	Meta        bool   `json:"meta"`
	EventUnixMS int64  `json:"event_unix_ms"`
}

func (event DesktopKeyboardEvent) Validate() error {
	if event.Code == "" || len(event.Code) > 64 || event.Location > 3 || event.EventUnixMS < 0 {
		return ErrDesktopKeyboardInvalid
	}
	return nil
}

type DesktopPointerEvent struct {
	DisplayID   string  `json:"display_id"`
	Generation  uint32  `json:"generation"`
	Kind        string  `json:"kind"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Button      int8    `json:"button"`
	Buttons     uint16  `json:"buttons"`
	WheelX      float64 `json:"wheel_x,omitempty"`
	WheelY      float64 `json:"wheel_y,omitempty"`
	EventUnixMS int64   `json:"event_unix_ms"`
}

func (event DesktopPointerEvent) Validate() error {
	if event.DisplayID == "" || len(event.DisplayID) > 128 || event.Generation == 0 ||
		(event.Kind != "move" && event.Kind != "down" && event.Kind != "up" && event.Kind != "wheel") ||
		math.IsNaN(event.X) || math.IsInf(event.X, 0) || event.X < 0 || event.X > 1 || math.IsNaN(event.Y) || math.IsInf(event.Y, 0) || event.Y < 0 || event.Y > 1 ||
		math.IsNaN(event.WheelX) || math.IsInf(event.WheelX, 0) || math.Abs(event.WheelX) > DesktopMaxWheelDelta ||
		math.IsNaN(event.WheelY) || math.IsInf(event.WheelY, 0) || math.Abs(event.WheelY) > DesktopMaxWheelDelta ||
		event.Button < -1 || event.Button > 4 || event.Buttons > 31 || event.EventUnixMS < 0 {
		return ErrDesktopPointerInvalid
	}
	return nil
}

type DesktopKeyboardInput = DesktopKeyboardEvent
type DesktopPointerInput = DesktopPointerEvent
type DesktopClipboardText struct {
	Direction string `json:"direction"`
	Text      string `json:"text"`
}

func (value DesktopClipboardText) Validate() error {
	if (value.Direction != "browser_to_agent" && value.Direction != "agent_to_browser") || len([]byte(value.Text)) > 1<<20 {
		return ErrDesktopClipboardInvalid
	}
	return nil
}

type DesktopControlMode struct {
	Enabled    bool   `json:"enabled"`
	FocusLease uint64 `json:"focus_lease"`
}

func (value DesktopControlMode) Validate() error {
	if value.Enabled && value.FocusLease == 0 {
		return ErrDesktopControlModeInvalid
	}
	return nil
}

type DesktopDisplaySelection struct {
	DisplayID  string `json:"display_id"`
	Generation uint32 `json:"generation"`
}

func (value DesktopDisplaySelection) Validate() error {
	if value.DisplayID == "" || len(value.DisplayID) > 128 || value.Generation == 0 {
		return ErrDesktopDisplaySelectionInvalid
	}
	return nil
}

type DesktopSpecialKey struct {
	Name string `json:"name"`
}

func (value DesktopSpecialKey) Validate() error {
	switch value.Name {
	case "alt_tab", "windows_l", "ctrl_alt_delete", "command_space", "command_option_escape", "command_control_q":
		return nil
	}
	return ErrDesktopSpecialKeyInvalid
}

type DesktopQuality struct {
	Profile string `json:"profile"`
}

type DesktopQualityLevel struct {
	Name       string  `json:"name"`
	Scale      float64 `json:"scale"`
	FPS        uint8   `json:"fps"`
	BitrateBPS uint32  `json:"bitrate_bps"`
}

var DesktopQualityLevels = []DesktopQualityLevel{
	{Name: "low", Scale: .5, FPS: 15, BitrateBPS: 1_000_000},
	{Name: "balanced", Scale: .75, FPS: 30, BitrateBPS: 4_000_000},
	{Name: "high", Scale: 1, FPS: 30, BitrateBPS: 8_000_000},
	{Name: "ultra", Scale: 1, FPS: 60, BitrateBPS: 20_000_000},
}

type DesktopPing struct {
	ID           string `json:"id"`
	SentAtUnixMS int64  `json:"sent_at_unix_ms"`
}
type DesktopStatistics struct {
	Frames                 uint64 `json:"frames,omitempty"`
	Bytes                  uint64 `json:"bytes,omitempty"`
	RTTMS                  uint32 `json:"rtt_ms"`
	DecoderQueue           uint32 `json:"decoder_queue"`
	DecodedFrames          uint64 `json:"decoded_frames"`
	DroppedFrames          uint64 `json:"dropped_frames"`
	SenderQueueBytes       uint64 `json:"sender_queue_bytes"`
	RelayBackpressureCount uint64 `json:"relay_backpressure_count"`
	EncodedBitrateBPS      uint32 `json:"encoded_bitrate_bps"`
	AppliedWidth           uint32 `json:"applied_width"`
	AppliedHeight          uint32 `json:"applied_height"`
}

func (value DesktopStatistics) Validate() error {
	if value.RTTMS > 300_000 || value.DecoderQueue > 1_000_000 || value.SenderQueueBytes > DesktopMaxQualityQueueBytes || value.EncodedBitrateBPS > 20_000_000 ||
		value.AppliedWidth > 16_384 || value.AppliedHeight > 16_384 {
		return ErrDesktopQualityInvalid
	}
	return nil
}

type DesktopState struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}
type DesktopTermination struct {
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message,omitempty"`
}

func (h DesktopInnerHeader) String() string {
	return fmt.Sprintf("desktop.v%d type=%d length=%d", h.Version, h.Type, h.PayloadLength)
}
