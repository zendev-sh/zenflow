# zenflow Plugin System

zenflow supports loading external tool plugins compiled as Go shared objects (`.so` files).

## Plugin Contract

A plugin must be a Go package compiled with `-buildmode=plugin` and must export a function with the exact signature:

```go
func GetTools() ([]goai.Tool, error)
```

The symbol name must be exactly `GetTools`.

## Build Command

```sh
go build -buildmode=plugin -o myplugin.so .
```

See `plugin_example/example.go` for a minimal working plugin.

## Using Plugins with `--plugin-dir`

Place your compiled `.so` files in a directory and pass it to any zenflow subcommand:

```sh
zenflow agent "do something" --plugin-dir ./my-plugins/
zenflow flow workflow.yaml --plugin-dir ./my-plugins/
zenflow goal "achieve something" --plugin-dir ./my-plugins/
```

The default plugin directory is `./plugins/`. If the directory does not exist, it is silently ignored. If a plugin fails to load or its `GetTools` returns an error, a warning is printed to stderr and that plugin is skipped — other plugins and built-in tools are unaffected.

## Version Compatibility Warning

Plugins must be compiled with the **exact same version of `github.com/zendev-sh/goai`** as the zenflow binary. A mismatch will cause the plugin to fail to load at runtime with a linker error. Always rebuild plugins when upgrading zenflow.
