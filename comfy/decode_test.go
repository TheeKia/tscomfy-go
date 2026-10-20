package comfy

import (
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"regexp"
	"slices"
	"testing"

	"github.com/TheeKia/tscomfy-go/internal/comfytest"
)

func TestDecodeFrame(t *testing.T) {
	preview := comfytest.PreviewFrame([]byte(`{"node_id":"5.0.1","display_node_id":"5","prompt_id":"p","image_type":"image/png"}`), []byte("PNG"))
	f, ok := decodeFrame(preview)
	if !ok || f.text || f.promptID != "p" || f.node != "5" || f.mime != "image/png" || string(f.data) != "PNG" {
		t.Errorf("a preview: %+v, %v", f, ok)
	}
	f, ok = decodeFrame(comfytest.PreviewFrame([]byte(`{"node_id":"7","prompt_id":"p"}`), nil))
	if !ok || f.node != "7" || f.mime != "image/jpeg" {
		t.Errorf("a preview without display id or type: %+v, %v", f, ok)
	}
	f, ok = decodeFrame(comfytest.TextFrame("9", "Downloading…"))
	if !ok || !f.text || f.promptID != "" || f.node != "9" || string(f.data) != "Downloading…" {
		t.Errorf("progress text: %+v, %v", f, ok)
	}
	legacy := binary.BigEndian.AppendUint32(nil, 1)
	legacy = binary.BigEndian.AppendUint32(legacy, 1)
	lying := binary.BigEndian.AppendUint32(nil, 4)
	lying = binary.BigEndian.AppendUint32(lying, 1000)
	for name, b := range map[string][]byte{
		"empty":              nil,
		"short":              {0, 0},
		"a legacy preview":   legacy,
		"an unknown type":    binary.BigEndian.AppendUint32(nil, 9),
		"a lying length":     lying,
		"no prompt id":       comfytest.PreviewFrame([]byte(`{"node_id":"1"}`), nil),
		"no node":            comfytest.PreviewFrame([]byte(`{"prompt_id":"p"}`), nil),
		"metadata not JSON":  comfytest.PreviewFrame([]byte(`{`), nil),
		"text without a len": binary.BigEndian.AppendUint32(nil, 3),
	} {
		if f, ok := decodeFrame(b); ok {
			t.Errorf("%s decoded to %+v", name, f)
		}
	}
}

func FuzzDecodeFrame(f *testing.F) {
	f.Add(comfytest.PreviewFrame([]byte(`{"node_id":"1","prompt_id":"p"}`), []byte("x")))
	f.Add(comfytest.TextFrame("1", "hi"))
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, ok := decodeFrame(b)
		if !ok {
			return
		}
		if fr.node == "" || (!fr.text && fr.promptID == "") || len(fr.data) > len(b) {
			t.Fatalf("decoded %+v from %d bytes", fr, len(b))
		}
	})
}

func TestParseNodeOutput(t *testing.T) {
	ui := jsontext.Value(`{
		"images": [{"filename": "a.png", "subfolder": "s", "type": "output", "metadata": {"kind": "image"}},
		           {"filename": "", "type": "output"}, {"filename": "b.png", "type": "elsewhere"}, "a string"],
		"gifs": [{"filename": "c.webp", "type": "temp"}],
		"text": ["hello", 1, "world"],
		"animated": [true],
		"other": {"filename": "not a list", "type": "output"}
	}`)
	out, ok := parseNodeOutput("9", ui)
	if !ok {
		t.Fatal("not parsed")
	}
	want := []OutputFile{
		{Filename: "a.png", Subfolder: "s", Type: FolderOutput, Key: "images"},
		{Filename: "c.webp", Type: FolderTemp, Key: "gifs"},
	}
	if out.Node != "9" || !slices.Equal(out.Files, want) || !slices.Equal(out.Text, []string{"hello", "world"}) {
		t.Errorf("%+v", out)
	}
	for _, v := range []string{`null`, `[]`, `"x"`, `{`} {
		if _, ok := parseNodeOutput("1", jsontext.Value(v)); ok {
			t.Errorf("%s parsed as an output", v)
		}
	}
}

func FuzzParseNodeOutput(f *testing.F) {
	f.Add([]byte(`{"images":[{"filename":"a.png","subfolder":"","type":"output"}],"text":["t"]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		out, ok := parseNodeOutput("n", jsontext.Value(b))
		if !ok {
			return
		}
		for _, file := range out.Files {
			if file.Filename == "" || !file.Type.valid() {
				t.Fatalf("a file %+v from %s", file, b)
			}
		}
	})
}

func TestNodeErrorsAreInPromptOrder(t *testing.T) {
	v := jsontext.Value(`{
		"9": {"errors": [{"type": "value_not_in_list", "message": "Value not in list", "details": "x", "extra_info": {"input_name": "ckpt"}}], "class_type": "Loader"},
		"2": {"errors": [{"type": "a", "message": "m"}, {"type": "b", "message": "n"}], "class_type": "Sampler"},
		"x": {"errors": [{"type": "c"}], "class_type": "Unknown"}
	}`)
	got := nodeErrors(v, map[NodeID]int{"2": 0, "9": 1})
	var nodes []NodeID
	for _, e := range got {
		nodes = append(nodes, e.Node)
	}
	if !slices.Equal(nodes, []NodeID{"2", "2", "9", "x"}) || got[2].Input != "ckpt" || got[2].ClassType != "Loader" {
		t.Errorf("%+v", got)
	}
	if got := nodeErrors(nil, nil); got != nil {
		t.Errorf("no node errors: %+v", got)
	}
}

func TestRejectedReadsStringsAndObjects(t *testing.T) {
	e, ok := errors.AsType[*RejectedError](rejected(jsontext.Value(`{"error":"plain words","node_errors":{}}`), nil))
	if !ok || e.Message != "plain words" {
		t.Errorf("%+v", e)
	}
	e, ok = errors.AsType[*RejectedError](rejected(jsontext.Value(`{"error":{"type":"missing_node_type","message":"Node not found","details":"Node not found","extra_info":{"node_id":"4"}}}`), nil))
	if !ok || e.Node != "4" || e.Error() != "comfy: the server refused the prompt: missing_node_type: Node not found" {
		t.Errorf("%+v: %v", e, e)
	}
}

func FuzzDispatch(f *testing.F) {
	f.Add([]byte(`{"type":"executing","data":{"node":null,"prompt_id":"p"}}`))
	f.Add([]byte(`{"type":"progress_state","data":{"prompt_id":"p","nodes":{"1":{"value":1,"max":2}}}}`))
	f.Fuzz(func(_ *testing.T, b []byte) {
		c := &Client{queue: make(chan struct{}, 1), jobs: map[string]*Job{}}
		j := c.newJob([]NodeID{"1"})
		j.id = "p"
		c.jobs["p"] = j
		c.dispatch(b) // never panics, whatever the message
	})
}

func TestNewUUID(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 1000 {
		id := newUUID()
		if !pattern.MatchString(id) || seen[id] {
			t.Fatalf("%q", id)
		}
		seen[id] = true
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string][3]int{"0.42.0": {0, 42, 0}, "v1.2.3": {1, 2, 3}, "0.42.0-3-g8f04fd6e": {0, 42, 0}} {
		if got, ok := parseVersion(in); !ok || got != want {
			t.Errorf("parseVersion(%q) = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "0.42", "x0.42.0", "unknown"} {
		if _, ok := parseVersion(in); ok {
			t.Errorf("parseVersion(%q) parsed", in)
		}
	}
}
