# WAN-01-A NDT7 Focused PoC

This module is an offline, focused verification of the M-Lab NDT7 protocol
contract that the future GoSpeed Provider integration must honour. It is the
WAN-01-A deliverable; the broader P0-I/P0-J/P0-K work lives in
`experiments/mlab-poc/`.

The module:

- Pins `github.com/m-lab/ndt7-client-go` to `v0.10.1` (same version the
  previous PoC validated).
- Uses an in-tree WebSocket mock server. Tests never reach M-Lab or any
  public service.
- Verifies exactly the items the WAN-01-A task explicitly listed:
  subprotocol handshake, download data frames, upload data frames, byte
  counting, timing window, cancellation latency, timeout behaviour,
  connection error handling, resource cleanup.
- Performs **no** Locate v2 query, **no** public M-Lab connection, and
  **no** automatic retry or repeat.

The PoC explicitly enumerates the budget / cancellation controls that the
official SDK supports and the ones it does not, so the WAN-01-B plan knows
which controls must be enforced above the SDK and which the SDK can carry.

## Run

```powershell
cd experiments/wan01a-poc
go mod download
go test ./...
go test -race ./...
go vet ./...
```
