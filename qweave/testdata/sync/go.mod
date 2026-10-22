module example.com/syncfixture

go 1.26.1

require github.com/casselc/quint-connect-go v0.0.0

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
)

replace github.com/casselc/quint-connect-go => ../../..
