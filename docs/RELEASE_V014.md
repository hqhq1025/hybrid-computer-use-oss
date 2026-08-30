# Computer Use v0.14

Release date: August 29, 2026.

## Frozen Identity

- Product version: `v0.14`
- MCP component:
  `0.70.0-beta.70+osw-v013-gui-native-1080p`
- Public tools: `AX`, `GUI`
- Source tree SHA-256:
  `32325485886f2575c50261190a65722b291f234c33c23da1df0b90ede4eec9a7`
- Promoted binary SHA-256:
  `11adcefd856200eda37ec127b586d95105f1d9b52c64e6ee8ff5571c1f5bacf0`
- Hybrid skill SHA-256:
  `61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16`

The immutable source and release manifest are under `computer-use/` and
`releases/v0.14/`.

## Delta From v0.13

The public MCP schema is unchanged. The Linux GUI delivery maximum changed from
`1366x768` to `1920x1080`. The standard OSWorld desktop is therefore returned
at native resolution with one-to-one public/native coordinates. Larger
desktops remain aspect-ratio bounded.

The stale-layout fail-closed behavior, action batching, AX identity model, and
provider protocol remain unchanged.

## Validation At Promotion

- focused release tests: 4/4;
- Go tests: passed;
- Go race and vet: passed;
- Linux runtime tests: 453/453;
- real-container native/declared/decoded screenshot: `1920x1080`.

No independent full benchmark was run for v0.14. Capability-score evidence was
inherited from v0.13; the release should not be described as having a new
v0.14 benchmark score.
