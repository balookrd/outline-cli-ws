module outline-cli-ws

// NOTE: The original `go` directive was set to a future Go toolchain version.
// In offline/CI environments this triggers an automatic toolchain download
// ("go: downloading goX.Y.Z"), which fails without network access.
// The project builds with Go 1.23+.
go 1.23.1

toolchain go1.23.2

require (
	github.com/coder/websocket v1.8.14
	github.com/shadowsocks/go-shadowsocks2 v0.1.5
	github.com/songgao/water v0.0.0-20200317203138-2b4b6d7c09d8
	gopkg.in/yaml.v3 v3.0.1
	gvisor.dev/gvisor v0.0.0-20250523182742-eede7a881b20
)

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/riobard/go-bloom v0.0.0-20200614022211-cdc8013cb5b3 // indirect
	golang.org/x/crypto v0.28.0 // indirect
	golang.org/x/sys v0.26.0 // indirect
	golang.org/x/time v0.7.0 // indirect
)
