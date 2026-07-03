module github.com/elijahmontenegro/grudge/rrc

go 1.25.0

require (
	github.com/elijahmontenegro/grudge/proto v0.0.0
	github.com/pkoukk/tiktoken-go v0.1.8
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
)

replace github.com/elijahmontenegro/grudge/proto => ../proto
