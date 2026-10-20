package comfytest

import (
	"embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

//go:embed recordings/*.json
var recordings embed.FS

// Run is one run of a protocol recording: the prompt, the answer to POST /prompt, the websocket
// events in order, and the job record at the end. PROMPT_ID stands for the prompt id the client
// minted; binary frames are {"binary": 4, "metadata", "image"} (base64) or {"binary": 3, "node",
// "text"}.
type Run struct {
	Prompt jsontext.Value   `json:"prompt"`
	Answer Exchange         `json:"answer"`
	Events []jsontext.Value `json:"events"`
	Record *Exchange        `json:"record"`
}

// Exchange is an HTTP answer: its status and body.
type Exchange struct {
	Status int            `json:"status"`
	Body   jsontext.Value `json:"body"`
}

// Recording returns the runs of a recording in recordings/, by name: "success", "rejected",
// "partial", "error", "cancel", "pending", "sampler", "websocket", "blocked". It panics for a name
// that doesn't exist: recordings are fixed test data.
func Recording(name string) []Run {
	data, err := recordings.ReadFile("recordings/" + name + ".json")
	if err != nil {
		panic(fmt.Sprintf("comfytest: no recording %q", name))
	}
	var file struct {
		Runs []Run `json:"runs"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		panic(fmt.Sprintf("comfytest: recording %q: %v", name, err))
	}
	return file.Runs
}
