# M-Lab NDT7 Isolated PoC

This module is an offline experiment for the GoSpeed P0-I phase. It is not
wired into the GoSpeed root module, the desktop module, the CLI, the GUI, or
the production speed-test engine.

The module pins `github.com/m-lab/ndt7-client-go` to `v0.10.1`. Its tests use
a local WebSocket fixture and never run a public M-Lab measurement.

The package deliberately keeps NDT7 metrics separate from the existing
GoSpeed and Cloudflare result models:

- `DownloadGoodputMbps`
- `UploadGoodputMbps`
- `TCPMinRTTMs`
- `RTTVarMs`
- `JitterMs` (always `nil` until an independent jitter measurement exists)

Run the offline checks from this directory:

```powershell
go mod download
go test ./...
go vet ./...
```

