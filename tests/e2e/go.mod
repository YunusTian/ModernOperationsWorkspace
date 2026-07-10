module github.com/mow/mow/tests/e2e

go 1.23.0

require (
	github.com/gliderlabs/ssh v0.3.7
	github.com/mow/mow/core v0.0.0-00010101000000-000000000000
	github.com/mow/mow/sdk v0.0.0-00010101000000-000000000000
	github.com/pkg/sftp v1.13.10
	golang.org/x/crypto v0.41.0
)

require (
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be // indirect
	github.com/fatih/color v1.7.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v0.14.1 // indirect
	github.com/hashicorp/go-plugin v1.6.2 // indirect
	github.com/hashicorp/yamux v0.1.1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/mattn/go-colorable v0.1.4 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/oklog/run v1.0.0 // indirect
	golang.org/x/net v0.42.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/text v0.28.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
	google.golang.org/grpc v1.68.1 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

replace (
	github.com/mow/mow/core => ../../core
	github.com/mow/mow/sdk => ../../sdk
)
