//go:build otel

// trace_otel.go - real OTel wiring for the `-tags otel` build path.
// Default builds (no build tag) ship a no-op `traceAppendOptionsFunc`
// + `withDefaultExporterFunc` defined in main.go, so the
// `github.com/zendev-sh/zenflow/observability/otel` submodule never
// enters the build graph. That keeps `go install
// github.com/zendev-sh/zenflow/cmd/zenflow@<version>` working before
// the submodule itself is tagged at the same version.
// Distributed binaries (Homebrew, GoReleaser releases, GHCR Docker
// images) are built with `-tags otel`, so end users who download the
// official artefacts get full --trace support out of the box. Source
// builds opt in with:
//	go install -tags otel github.com/zendev-sh/zenflow/cmd/zenflow@<version>
// Without the tag, `--trace` is silently a no-op (the flag still
// parses to keep the CLI surface stable).

package main

import (
	"context"
	"os"

	"github.com/zendev-sh/zenflow"
	zenotel "github.com/zendev-sh/zenflow/observability/otel"
)

func init() {
	traceAppendOptionsFunc = func(opts []zenflow.Option) []zenflow.Option {
		opts = append(opts, zenotel.WithTracing())
		opts = append(opts, zenflow.WithGoAIOptions(zenotel.GoAIOption()))
		return opts
	}
	withDefaultExporterFunc = func(ctx context.Context) (func(context.Context) error, error) {
		_, shutdown, err := zenotel.WithDefaultExporter(ctx, os.Stderr)
		return shutdown, err
	}
}
