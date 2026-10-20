package comfy

import (
	"encoding/binary"
	"encoding/json/v2"
)

// Binary frame types (docs/spec.md §8, rule 7).
const (
	frameText    = 3 // progress text: no prompt id, broadcast
	framePreview = 4 // a preview with metadata
)

// frame is a decoded binary frame: a preview, or progress text.
type frame struct {
	text     bool
	promptID string // a preview's; "" for progress text
	node     NodeID
	mime     string // a preview's
	data     []byte // the image, or the text
}

// decodeFrame decodes a binary websocket frame: a big-endian uint32 type, then the payload. Type
// 1, a preview without ids, never comes, since the client announces preview metadata. Other
// types, and frames whose lengths don't fit, give ok false.
func decodeFrame(b []byte) (f frame, ok bool) {
	if len(b) < 4 {
		return frame{}, false
	}
	switch binary.BigEndian.Uint32(b) {
	case framePreview:
		return decodePreview(b[4:])
	case frameText:
		return decodeText(b[4:])
	default:
		return frame{}, false
	}
}

// decodePreview reads a uint32 length, that many bytes of JSON metadata, then the image.
func decodePreview(b []byte) (frame, bool) {
	meta, image, ok := lengthPrefixed(b)
	if !ok {
		return frame{}, false
	}
	var m struct {
		PromptID      string `json:"prompt_id"`
		NodeID        string `json:"node_id"`
		DisplayNodeID string `json:"display_node_id"`
		ImageType     string `json:"image_type"`
	}
	if json.Unmarshal(meta, &m, lenient) != nil || m.PromptID == "" {
		return frame{}, false
	}
	node := m.DisplayNodeID
	if node == "" {
		node = m.NodeID
	}
	if node == "" {
		return frame{}, false
	}
	mime := m.ImageType
	if mime == "" {
		mime = "image/jpeg"
	}
	return frame{promptID: m.PromptID, node: NodeID(node), mime: mime, data: image}, true
}

// decodeText reads a uint32 length, the node id, then the text.
func decodeText(b []byte) (frame, bool) {
	node, text, ok := lengthPrefixed(b)
	if !ok || len(node) == 0 {
		return frame{}, false
	}
	return frame{text: true, node: NodeID(node), data: text}, true
}

// lengthPrefixed splits b after a big-endian uint32 length and that many bytes.
func lengthPrefixed(b []byte) (head, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}
