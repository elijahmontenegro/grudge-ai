module github.com/emontenegr/spidey/service

go 1.25.0

require (
	fyne.io/systray v1.12.0
	github.com/99designs/gqlgen v0.17.89
	github.com/asg017/sqlite-vec-go-bindings v0.1.6
	github.com/emontenegr/spidey/core v0.0.0-00010101000000-000000000000
	github.com/emontenegr/spidey/gen/go v0.0.0
	github.com/emontenegr/spidey/rrc v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/modelcontextprotocol/go-sdk v1.5.0
	github.com/ncruces/go-sqlite3 v0.20.3
	github.com/vektah/gqlparser/v2 v2.5.32
	github.com/zalando/go-keyring v0.2.8
	google.golang.org/adk v1.0.0
	google.golang.org/genai v1.40.0
	google.golang.org/protobuf v1.36.11
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.17.0 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/safehtml v0.1.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.6 // indirect
	github.com/googleapis/gax-go/v2 v2.15.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sosodev/duration v1.4.0 // indirect
	github.com/tetratelabs/wazero v1.8.2 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.63.0 // indirect
	go.opentelemetry.io/otel v1.40.0 // indirect
	go.opentelemetry.io/otel/log v0.16.0 // indirect
	go.opentelemetry.io/otel/metric v1.40.0 // indirect
	go.opentelemetry.io/otel/trace v1.40.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
	google.golang.org/grpc v1.79.3 // indirect
	rsc.io/omap v1.2.0 // indirect
	rsc.io/ordered v1.1.1 // indirect
)

// Replace directives for local development. Required (in addition to
// go.work) because `go build` from a module subdirectory resolves
// dependencies via the module proxy even under workspace mode. When
// these modules publish, drop the corresponding lines.
replace (
	github.com/emontenegr/spidey/core => ../core
	github.com/emontenegr/spidey/gen/go => ../gen/go
	github.com/emontenegr/spidey/rrc => ../rrc
)
