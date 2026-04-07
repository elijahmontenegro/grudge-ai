package graph

import "time"

// Thread is the GraphQL model for Thread.
type Thread struct {
	ID                    string     `json:"id"`
	Name                  string     `json:"name"`
	WorkingDirs           []string   `json:"workingDirs"`
	Sandboxed             bool       `json:"sandboxed"`
	CreatedAt             time.Time  `json:"createdAt"`
	ParentThreadID        *string    `json:"parentThreadId"`
	BranchPointPosition   *int       `json:"branchPointPosition"`
	ArchivedAt            *time.Time `json:"archivedAt"`
}

// Message is the GraphQL model for Message.
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Position  int       `json:"position"`
	ThreadID  string    `json:"threadId"`
	CreatedAt time.Time `json:"createdAt"`
}

// Edge is the GraphQL model for Edge.
type Edge struct {
	FromMessageID     string  `json:"fromMessageId"`
	ToMessageID       string  `json:"toMessageId"`
	Score             float64 `json:"score"`
	Source            string  `json:"source"`
	CrossEncoderScore float64 `json:"crossEncoderScore"`
	QudWeight         float64 `json:"qudWeight"`
	TemporalProximity float64 `json:"temporalProximity"`
}

// SelectedMessage is the GraphQL model for SelectedMessage.
type SelectedMessage struct {
	MessageID      string  `json:"messageId"`
	EffectiveScore float64 `json:"effectiveScore"`
	HopDepth       int     `json:"hopDepth"`
	ThreadID       string  `json:"threadId"`
	CrossThread    bool    `json:"crossThread"`
}

// ExcludedMessage is the GraphQL model for ExcludedMessage.
type ExcludedMessage struct {
	MessageID string  `json:"messageId"`
	Reason    string  `json:"reason"`
	Score     float64 `json:"score"`
}

// SelectionResult is the GraphQL model for SelectionResult.
type SelectionResult struct {
	EventID  string             `json:"eventId"`
	Scope    string             `json:"scope"`
	ThreadID string             `json:"threadId"`
	Selected []*SelectedMessage `json:"selected"`
	Excluded []*ExcludedMessage `json:"excluded"`
}

// QUD is the GraphQL model for QUD.
type QUD struct {
	ID            string   `json:"id"`
	Question      string   `json:"question"`
	EstablishedBy string   `json:"establishedBy"`
	ParentQudID   *string  `json:"parentQudId"`
	Status        string   `json:"status"`
	AddressedBy   []string `json:"addressedBy"`
}

// QUDGraph is the GraphQL model for QUDGraph.
type QUDGraph struct {
	Quds        []*QUD   `json:"quds"`
	ActiveStack []string `json:"activeStack"`
}
