package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"plugin"

	"github.com/zendev-sh/goai"
)

// LoadPlugins loads all .so plugin files from dir and returns their combined
// tools. If dir does not exist, an empty slice is returned with no error.
// Plugins that fail to load or return an error from GetTools are skipped with
// a warning printed to stderr.
func LoadPlugins(dir string) ([]goai.Tool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading plugin dir %q: %w", dir, err)
	}
	var tools []goai.Tool
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".so" {
			continue
		}
		soPath := filepath.Join(dir, entry.Name())
		plugTools, warn := loadPlugin(soPath)
		if warn != "" {
			fmt.Fprintln(os.Stderr, warn)
			continue
		}
		tools = append(tools, plugTools...)
	}
	return tools, nil
}

// loadPlugin opens a single .so file and calls its GetTools symbol.
// Returns the tools on success or a warning message (non-empty) on failure.
func loadPlugin(path string) ([]goai.Tool, string) {
	p, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Sprintf("zenflow: plugin %q: failed to open: %v", path, err)
	}
	sym, err := p.Lookup("GetTools")
	if err != nil {
		return nil, fmt.Sprintf("zenflow: plugin %q: symbol GetTools not found: %v", path, err)
	}
	fn, ok := sym.(func() ([]goai.Tool, error))
	if !ok {
		return nil, fmt.Sprintf("zenflow: plugin %q: GetTools has wrong signature (want func() ([]goai.Tool, error))", path)
	}
	tools, err := fn()
	if err != nil {
		return nil, fmt.Sprintf("zenflow: plugin %q: GetTools returned error: %v", path, err)
	}
	return tools, ""
}
