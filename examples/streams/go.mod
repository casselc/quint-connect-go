module github.com/casselc/quint-connect-go/examples/streams

go 1.26.1

require (
	github.com/aws/aws-sdk-go-v2 v1.47.3
	github.com/aws/aws-sdk-go-v2/service/kinesis v1.56.6
	github.com/casselc/quint-connect-go v0.0.0
	github.com/twmb/franz-go v1.22.2
	github.com/twmb/franz-go/pkg/kfake v0.0.0-20261020234411-c06330b92452
	github.com/twmb/franz-go/pkg/kmsg v1.15.0
	hegel.dev/go/hegel v0.9.27
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.6 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.6 // indirect
	github.com/aws/smithy-go v1.28.5 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/ebitengine/purego v0.12.0-alpha.1.0.20261015130536-d456b1bb95e8 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/casselc/quint-connect-go => ../..
