# Development

## Core Checks

```bash
make test
make build
make verify
make smoke
```

`make build-local` is convenient during development, but its binary hash can
differ across Go toolchain versions. `make build` uses the pinned Go 1.22 image
from the v0.14 release build and is the reproducibility path.

## Release Discipline

Keep three identities separate:

- product release, such as `v0.14`;
- an unreleased implementation change;
- an evaluation run ID.

Changing source, behavior, schemas, tool descriptions, runtime dependencies, or
security boundaries requires a new candidate identity and immutable artifact.
Changing model, prompt, skill, task set, timeout, or concurrency requires a new
run ID but does not by itself create a product release.

Do not rewrite `releases/v0.14/release.json`. It records historical promotion
evidence, including candidate-era labels that remain in the promoted artifact.
