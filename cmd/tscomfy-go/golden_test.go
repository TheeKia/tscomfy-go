package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/TheeKia/tscomfy-go/cmd/tscomfy-go/internal/golden"
	"github.com/TheeKia/tscomfy-go/workflow"
)

// The golden package compiles, its templates load at init, and its Build methods fill them.
func TestGoldenBuilds(t *testing.T) {
	steps, tiled := 30, true
	prompt, err := golden.EveryKind{Seed: 1 << 63, Steps: &steps, Offset: -1, Cfg: 7, Tiled: &tiled, SamplerName: "dpmpp_2m"}.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"seed":9223372036854775808`, `"steps":30`, `"offset":-1`, `"tiled":true`, `"sampler_name":"dpmpp_2m"`, `"denoise":1`} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("the prompt has no %s: %s", want, prompt)
		}
	}
	if _, err := (golden.EveryKind{Cfg: 101}).Build(); !errors.Is(err, workflow.ErrOutOfRange) {
		t.Errorf("Build with cfg 101: error = %v", err)
	}
	if prompt, err := (golden.Bare{}).Build(); err != nil || string(prompt) != `{"1":{"inputs":{},"class_type":"Save"}}` {
		t.Errorf("Bare.Build() = %s, %v", prompt, err)
	}
	if golden.EveryKindSampler != "sampler" || golden.EveryKindVaeDecode2X != "vae_decode2x" {
		t.Error("the node constants are wrong")
	}
}
