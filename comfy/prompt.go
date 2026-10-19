package comfy

import "encoding/json/jsontext"

// Prompt is an API-format prompt, raw: the JSON object that POST /prompt takes as "prompt". It is
// sent byte for byte and never decoded into Go values, so integers beyond 2^53, such as 64-bit
// seeds, stay exact. Generated Build methods return one; raw JSON from elsewhere converts with
// Prompt(raw).
type Prompt jsontext.Value

// NodeID is a node's key in a prompt. ComfyUI reports it in every event about the node.
type NodeID string
