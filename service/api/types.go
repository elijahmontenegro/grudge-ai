package api

// Attachment is the JSON shape POST /api/attachments/{threadId}
// returns and that the client echoes back as a GraphQL mutation
// input. Fields and JSON tags match the gqlgen-generated
// graph.AttachmentInput so the client can round-trip the JSON
// without translation. Defining it here lets api stand on its own —
// no inverted import of service/graph just to name the wire type.
type Attachment struct {
	ID          string  `json:"id"`
	Filename    string  `json:"filename"`
	MimeType    string  `json:"mimeType"`
	SizeBytes   int     `json:"sizeBytes"`
	Path        string  `json:"path"`
	InlinedText *string `json:"inlinedText,omitempty"`
}
