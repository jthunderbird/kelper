# kelper-fetch assets

`make build` compiles `cmd/kelper-fetch` for each supported node architecture
into this directory (`kelper-fetch-linux-amd64`, `kelper-fetch-linux-arm64`),
and `internal/pull` embeds them into the kelper binary. The binaries are build
outputs and are not committed.
