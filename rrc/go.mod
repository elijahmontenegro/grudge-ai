module github.com/emontenegr/spidey/rrc

go 1.24.2

require (
	github.com/emontenegr/spidey/gen/go v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/emontenegr/spidey/gen/go => ../gen/go
