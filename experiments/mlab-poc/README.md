# M-Lab NDT7 Isolated PoC

This module is an offline experiment for the GoSpeed P0-I / P0-J phases. It is
not wired into the GoSpeed root module, the desktop module, the CLI, the GUI,
or the production speed-test engine.

The module pins `github.com/m-lab/ndt7-client-go` to `v0.10.1`. Its tests use
a local WebSocket fixture and never run a public M-Lab measurement.

The package deliberately keeps NDT7 metrics separate from the existing
GoSpeed and Cloudflare result models:

- `DownloadGoodputMbps`
- `UploadGoodputMbps`
- `TCPMinRTTMs`
- `RTTVarMs`
- `JitterMs` (always `nil` until an independent jitter measurement exists)

## P0-J additions

- **Prompt cancellation** (`provider/conntrack.go`): every connection is
  dialed through the SDK's public `websocket.Dialer.NetDialContext` hook and
  tracked. Context cancellation (user cancel or overall timeout) force-closes
  the underlying transport, so SDK goroutines blocked in
  `NextReader` / `ReadMessage` / `WritePreparedMessage` return immediately
  instead of waiting up to 7 s for the SDK I/O deadline.
- **Wire-level byte budgets** (`Options.DownloadBudgetBytes`,
  `UploadBudgetBytes`, `Plan.TotalBudgetBytes`): bytes are counted at the
  socket layer. Download budgets stop reading and close the connection; upload
  budgets refuse writes before they reach the network. Budget-aborted runs are
  marked `budget_exceeded` and are never treated as complete NDT7 tests. These
  are experimental PoC budgets, not ISP billing limits.
- **Privacy consent** (`provider/consent.go`): explicit opt-in record with a
  policy version and timestamp, an interactive CLI prompt that defaults to
  "no", and an HTML consent page template. Without an agreed record no
  connection is created at all.
- **SBOM** (`cmd/sbomgen`, output in `sbom/cyclonedx.json`): CycloneDX 1.5
  JSON generated from the real module graph. Build-participating modules are
  `scope: required`; graph-only modules are `scope: optional`. Unconfirmed
  licenses are reported as `NOASSERTION`.
- **localhost-only CLI** (`cmd/mlabpoc`): demo runner that requires
  interactive consent and refuses any non-loopback target.

## P0-K additions

- **Locate discovery, consent-gated** (`provider/locate.go`): one Locate v2
  `Nearest` query through the official `m-lab/locate` client, always behind
  the privacy consent record. The default `http.DefaultClient` of the
  official client is replaced by a client with an explicit timeout. Every
  returned target must carry usable measurement URLs whose scheme is in an
  allow list (`wss` in production) and whose host matches a trusted suffix
  list (M-Lab operated hostnames, e.g. `*.measurement-lab.org`).
- **Locate mock server** (`mock/locate.go`): localhost scenarios covering
  normal results, empty lists, invalid targets, insecure URLs, HTTP 429/500,
  stalled bodies, slow headers, and connection/DNS failures.
- **Remote measurement gate** (`provider/remote.go`): a non-loopback target
  requires (1) an explicitly enabled experimental build switch, (2) a host
  derived from validated Locate discovery, and (3) a per-run confirmation
  that discloses public-data, traffic-budget and timeout risks. The switch
  defaults to disabled; a closed gate yields zero network activity.
- **Experimental remote entry point** (`cmd/mlabremote`): wires consent ->
  discovery -> per-run confirmation -> one measured plan. It does not enable
  the switch by default and was not used for a real measurement in P0-K.
- **Consent notice v2** (`gospeed-mlab-notice-2`): adds the Locate step and
  the planned traffic budget (24/16/40 MiB wire-level, experimental) and the
  overall timeout to the CLI notice and the HTML consent page.

Run the offline checks from this directory:

```powershell
go mod download
go test ./...
go test -race ./...
go vet ./...
go mod verify
go run ./cmd/sbomgen -root . -o sbom/cyclonedx.json
```
