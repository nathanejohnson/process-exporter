# Replace gopkg.in/yaml.v2 with goccy/go-yaml Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the archived `gopkg.in/yaml.v2` dependency with the actively maintained `github.com/goccy/go-yaml`.

**Architecture:** Single import swap in `config/config.go`. The `goccy/go-yaml` library's `InterfaceUnmarshaler` interface has the same signature as yaml.v2's custom unmarshaller, so no code changes are needed beyond the import path.

**Tech Stack:** Go, github.com/goccy/go-yaml

---

### Task 1: Swap the YAML dependency

**Files:**
- Modify: `config/config.go:15` (import statement)
- Modify: `go.mod` (dependency swap)
- Modify: `go.sum` (auto-generated)

- [ ] **Step 1: Add the new dependency**

Run:
```bash
go get github.com/goccy/go-yaml
```

Expected: `go.mod` gains `github.com/goccy/go-yaml` as a direct dependency.

- [ ] **Step 2: Update the import in config.go**

In `config/config.go`, change line 15 from:
```go
"gopkg.in/yaml.v2"
```
to:
```go
"github.com/goccy/go-yaml"
```

- [ ] **Step 3: Remove the old dependency**

Run:
```bash
go mod tidy
```

Expected: `gopkg.in/yaml.v2` is removed from `go.mod` (unless pulled in transitively by another dependency).

- [ ] **Step 4: Verify compilation**

Run:
```bash
go build ./...
```

Expected: Clean build, exit code 0.

- [ ] **Step 5: Run config package tests**

Run:
```bash
go test ./config/ -v
```

Expected: All 2 tests pass (TestConfigBasic, TestConfigTemplates).

- [ ] **Step 6: Run full test suite**

Run:
```bash
make test
```

Expected: All tests pass.

- [ ] **Step 7: Run build target**

Run:
```bash
make build
```

Expected: Binary compiles successfully.

- [ ] **Step 8: Commit**

```bash
git add config/config.go go.mod go.sum
git commit -m "deps: replace gopkg.in/yaml.v2 with github.com/goccy/go-yaml"
```
