# Go Package Split Implementation Plan

**Goal:** Turn the partially split Go rewrite into a compiling CLI while preserving its current placeholder behavior.

**Architecture:** `main` loads environment/configuration and wires dependencies. `config` parses flags, `cli` owns the REPL, `agent` owns conversation state, and `ui` owns terminal output.

**Tech Stack:** Go 1.26, standard library `flag`/`io`, `godotenv`.

## Tasks

1. Add testable argument parsing in `internal/config` and verify environment defaults and CLI overrides.
2. Add terminal output helpers in `internal/ui`.
3. Give `internal/agent` a constructor and minimal history/cost behavior.
4. Export a testable REPL in `internal/cli` using a small agent interface.
5. Update `main.go` to import and wire the packages.
6. Run `gofmt`, `go test ./...`, and `go vet ./...`.
