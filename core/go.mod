module github.com/emontenegr/spidey/core

go 1.25.0

require github.com/emontenegr/spidey/gen/go v0.0.0

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/emontenegr/spidey/gen/go => ../gen/go
