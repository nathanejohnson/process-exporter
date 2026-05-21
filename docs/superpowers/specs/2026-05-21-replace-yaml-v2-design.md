# Replace gopkg.in/yaml.v2 with github.com/goccy/go-yaml

## Summary

Replace the archived, unmaintained `gopkg.in/yaml.v2` YAML library with the actively maintained `github.com/goccy/go-yaml`. The `goccy/go-yaml` library was designed as a drop-in replacement for yaml.v2, providing the same `yaml` struct tags and a compatible `InterfaceUnmarshaler` interface.

## Motivation

`gopkg.in/yaml.v2` is no longer maintained. Its successor `gopkg.in/yaml.v3` is also archived. `github.com/goccy/go-yaml` is actively maintained, performant, and designed for API compatibility with yaml.v2.

## Scope

This change touches one application file and dependency management files only.

### Files Modified

1. **`config/config.go`** — Change import from `"gopkg.in/yaml.v2"` to `"github.com/goccy/go-yaml"`
2. **`go.mod` / `go.sum`** — Add new dependency, remove old one

### No Code Changes Required

- `yaml.Unmarshal()` — same function signature in both libraries
- `yaml:"..."` struct tags — supported as-is by `goccy/go-yaml`
- `Config.UnmarshalYAML(unmarshal func(v interface{}) error) error` — matches `goccy/go-yaml`'s `InterfaceUnmarshaler` interface exactly

## Alternatives Considered

| Library | Effort | Why not |
|---------|--------|---------|
| `gopkg.in/yaml.v3` | Low | Archived, no longer maintained |
| `sigs.k8s.io/yaml` | Medium-high | Requires changing all `yaml:` struct tags to `json:` and rewriting custom unmarshaller as `UnmarshalJSON` |

## Verification

- Run existing `config` package tests to confirm parsing behavior is unchanged
- Run `make test` for full test suite
- Run `make build` to confirm clean compilation
