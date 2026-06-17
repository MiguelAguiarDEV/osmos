package types

const MaxInlineBytes = 64 << 10 // 64 KiB

// WSReadLimit returns the websocket message read limit needed to receive a
// maximally-sized inline clip. Inline Data is base64-encoded inside the JSON
// envelope (~4/3 expansion); the headroom covers envelope keys, msg_id, mime
// and device_id. The coder/websocket default is only 32 KiB, so without raising
// it inline clips larger than ~24 KiB of raw data fail to read.
func WSReadLimit(maxInlineBytes int) int64 {
	if maxInlineBytes <= 0 {
		maxInlineBytes = MaxInlineBytes
	}
	return int64(maxInlineBytes+2)/3*4 + 4096
}

type Envelope struct {
	Type  string `json:"type"`
	From  string `json:"from,omitempty"` // deviceID del emisor
	Hello *Hello `json:"hello,omitempty"`
	Clip  *Clip  `json:"clip,omitempty"`
}

type Hello struct {
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id"`
}

type Clip struct {
	MsgID     string `json:"msg_id,omitempty"`
	Mime      string `json:"mime,omitempty"`
	Size      int    `json:"size,omitempty"`
	Data      []byte `json:"data,omitempty"`
	UploadURL string `json:"upload_url,omitempty"`
}
