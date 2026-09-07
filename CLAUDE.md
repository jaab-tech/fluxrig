# fluxrig

A protocol orchestration engine: route, transform and observe data streams across
heterogeneous environments, whether the flow is being designed now or has been in
production for a decade. Written in Go, extended with WebAssembly.

## Build and test

```bash
make build              # binaries into bin/
make test               # unit tests with -race and coverage
make lint               # golangci-lint, plus the repository guards
make test-robot         # the functional Robot Framework suites
```

`make build` needs `swag` on the PATH; it regenerates the OpenAPI spec.
The load and chaos suites run on their own targets, never inside `make test-robot`:
they are sized for a quiet machine and become noise inside a batch run.

## The model

| Term | What it is |
|:---|:---|
| **Gear** | One processing step. Native (Go) or Wasm. |
| **Rack** | An edge node running a scenario. Processes independently of the control plane. |
| **Mixer** | The control plane: enrollment, scenario deployment, telemetry. |
| **Scenario** | A YAML description of gears and how they are wired. |

A scenario is configuration, not code. Most behaviour changes should be a
scenario change; reach for a new gear only when the pipeline genuinely cannot
express it.

## Naming

- `fluxrig` is **always lowercase**, including at the start of a sentence.
- Components are PascalCase: `Mixer`, `Rack`, `Gear`.
- Data primitives are camelCase: `fluxMsg`, `fluxID`.
- Edge nodes are **Racks**, never "agents".

## Code standards

- **No hardcoded configuration.** Durations, ports, sizes and limits are config
  fields with documented defaults, never literals in the logic.
- **Every wait has a timeout**, and the timeout is configurable.
- **Never `time.Sleep` to wait for a condition in a test.** Poll with a deadline;
  a sleep encodes the speed of the machine that happened to run it.
- Configuration tables in documentation must match the `koanf`/`json` tags in the
  Go source. They drift silently otherwise.

## Where to look

- `pkg/gears/native/` — the built-in gears
- `pkg/sdk/` — what a gear can do
- `examples/scenarios/` — working scenarios, including the ones the docs walk through
- `test/robot/suites/` — integration suites, one directory each
- `.claude/skills/` — task-specific procedures, loaded when relevant
- [fluxrig.org/docs](https://fluxrig.org/docs) — reference and tutorials
