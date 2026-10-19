// Command txt2img prints the txt2img prompt for a text and a seed, built in Go from the template
// that go generate wrote from workflows/txt2img.ts.
//
//	go run ./examples/txt2img "a fox in the snow" 42
package main

//go:generate go tool tscomfy-go -workflows ./workflows -out ./internal/workflows

import (
	"fmt"
	"os"
	"strconv"

	"github.com/TheeKia/tscomfy-go/examples/txt2img/internal/workflows"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: txt2img <prompt> <seed>")
		os.Exit(2)
	}
	seed, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "txt2img: the seed is not an unsigned integer")
		os.Exit(2)
	}
	prompt, err := workflows.Txt2Img{Prompt: os.Args[1], Seed: seed, Steps: new(20)}.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, "txt2img:", err)
		os.Exit(1)
	}
	fmt.Println(string(prompt))
}
