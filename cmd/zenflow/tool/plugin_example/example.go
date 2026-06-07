//go:build ignore

// Package main is an example zenflow plugin that exports a simple "echo" tool.
// Compile with: go build -buildmode=plugin -o example.so .
package main

import (
	"context"

	"github.com/zendev-sh/goai"
)

// GetTools returns the tools provided by this plugin.
// This symbol must be exported exactly as `func() ([]goai.Tool, error)`.
func GetTools() ([]goai.Tool, error) {
	echoTool := goai.NewTool(
		"echo",
		"Returns the input string unchanged. Useful for testing plugin loading.",
		func(_ context.Context, p struct {
			Input string `json:"input" jsonschema:"description=The string to echo back"`
		}) (string, error) {
			return p.Input, nil
		},
	)
	return []goai.Tool{echoTool}, nil
}
