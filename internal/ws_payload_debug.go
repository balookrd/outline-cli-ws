package internal

import (
	"encoding/hex"
)

const wsPayloadDebugPreviewBytes = 48

func wsDebugPayload(direction, upstream, proto string, payload []byte) {
	if !wsDebugEnabled.Load() {
		return
	}
	previewLen := len(payload)
	if previewLen > wsPayloadDebugPreviewBytes {
		previewLen = wsPayloadDebugPreviewBytes
	}
	preview := hex.EncodeToString(payload[:previewLen])
	if len(payload) > previewLen {
		wsDebugf("ws payload %s upstream=%q proto=%q len=%d preview_hex=%s...", direction, upstream, proto, len(payload), preview)
		return
	}
	wsDebugf("ws payload %s upstream=%q proto=%q len=%d preview_hex=%s", direction, upstream, proto, len(payload), preview)
}
