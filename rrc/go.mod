module github.com/emontenegr/spidey/rrc

go 1.25.0

require (
	github.com/emontenegr/spidey/gen/go v0.0.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8 // indirect
)

replace github.com/emontenegr/spidey/gen/go => ../gen/go
