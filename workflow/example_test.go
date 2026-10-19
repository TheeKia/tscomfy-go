package workflow_test

import (
	"errors"
	"fmt"

	"github.com/TheeKia/tscomfy-go/workflow"
)

// A template as the generator writes it, here inline: a blur whose radius is a parameter.
var blur = []byte(`{
  "format": 1,
  "name": "blur",
  "params": [
    { "name": "radius", "node": "blur", "input": "blur_radius", "kind": "int", "min": 1, "max": 31 }
  ],
  "prompt": {
    "load": { "inputs": { "image": "photo.png" }, "class_type": "LoadImage" },
    "blur": { "inputs": { "blur_radius": 1, "sigma": 1, "image": ["load", 0] }, "class_type": "ImageBlur" },
    "save": { "inputs": { "filename_prefix": "blur", "images": ["blur", 0] }, "class_type": "SaveImage" }
  }
}`)

func ExampleLoad() {
	tmpl, err := workflow.Load(blur)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, p := range tmpl.Params() {
		fmt.Printf("%s: %s.%s, %s from %s to %s, default %s\n", p.Name, p.Node, p.Input, p.Kind, p.Min, p.Max, p.Default)
	}
	// Output:
	// radius: blur.blur_radius, int from 1 to 31, default 1
}

func ExampleTemplate_Fill() {
	tmpl := workflow.MustLoad(blur)
	prompt, err := tmpl.Fill(5)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(prompt))

	_, err = tmpl.Fill(40)
	fmt.Println(err)
	fmt.Println(errors.Is(err, workflow.ErrOutOfRange))
	// Output:
	// {"load":{"inputs":{"image":"photo.png"},"class_type":"LoadImage"},"blur":{"inputs":{"blur_radius":5,"sigma":1,"image":["load",0]},"class_type":"ImageBlur"},"save":{"inputs":{"filename_prefix":"blur","images":["blur",0]},"class_type":"SaveImage"}}
	// workflow blur: parameter radius: the value is outside the input's bounds: 40 is not within 1 to 31
	// true
}
