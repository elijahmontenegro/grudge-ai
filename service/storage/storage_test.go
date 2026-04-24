package storage

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// --- Thread tests ---

func TestCreateAndGetThread(t *testing.T) {
	db := testDB(t)

	thread := &pb.Thread{
		Id:        "t1",
		Name:      "Test Thread",
		Sandboxed: true,
		CreatedAt: timestamppb.Now(),
	}
	if err := db.CreateThread(thread); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetThread("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Id != "t1" || got.Name != "Test Thread" || !got.Sandboxed {
		t.Fatalf("unexpected thread: %+v", got)
	}
}

func TestCreateThread_WithWorkingDirs(t *testing.T) {
	db := testDB(t)

	thread := &pb.Thread{
		Id:          "t1",
		Name:        "Test",
		WorkingDirs: []string{"/home/user/project", "/tmp/scratch"},
		CreatedAt:   timestamppb.Now(),
	}
	if err := db.CreateThread(thread); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetThread("t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WorkingDirs) != 2 {
		t.Fatalf("expected 2 working dirs, got %d", len(got.WorkingDirs))
	}
}

func TestListThreads(t *testing.T) {
	db := testDB(t)

	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		db.CreateThread(&pb.Thread{
			Id: name, Name: name, CreatedAt: timestamppb.Now(),
		})
		_ = i
	}

	threads, err := db.ListThreads(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 3 {
		t.Fatalf("expected 3 threads, got %d", len(threads))
	}
}

func TestArchiveThread(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})

	if err := db.ArchiveThread("t1"); err != nil {
		t.Fatal(err)
	}

	// Not in unarchived list
	threads, _ := db.ListThreads(false)
	if len(threads) != 0 {
		t.Fatal("archived thread should not appear in unarchived list")
	}

	// In full list
	threads, _ = db.ListThreads(true)
	if len(threads) != 1 {
		t.Fatal("archived thread should appear when includeArchived=true")
	}
	if threads[0].ArchivedAt == nil {
		t.Fatal("ArchivedAt should be set")
	}
}

func TestUnarchiveThread(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})
	db.ArchiveThread("t1")
	db.UnarchiveThread("t1")

	threads, _ := db.ListThreads(false)
	if len(threads) != 1 {
		t.Fatal("unarchived thread should appear in list")
	}
}

func TestDeleteThread(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})

	if err := db.DeleteThread("t1"); err != nil {
		t.Fatal(err)
	}

	_, err := db.GetThread("t1")
	if err == nil {
		t.Fatal("deleted thread should not be found")
	}
}

func TestUpdateThreadName(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Old Name", CreatedAt: timestamppb.Now()})

	if err := db.UpdateThreadName("t1", "New Name"); err != nil {
		t.Fatal(err)
	}

	got, _ := db.GetThread("t1")
	if got.Name != "New Name" {
		t.Fatalf("expected 'New Name', got %q", got.Name)
	}
}

// --- Message tests ---

func TestInsertAndGetMessage(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})

	msg := &pb.Message{
		Id:       "m1",
		ThreadId: "t1",
		Role:     pb.Role_ROLE_USER,
		Content: []*pb.ContentBlock{
			{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "Hello world"}}},
		},
		Position:  0,
		CreatedAt: timestamppb.Now(),
	}
	if err := db.InsertMessage(msg); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetMessage("m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Id != "m1" || got.ThreadId != "t1" || got.Role != pb.Role_ROLE_USER {
		t.Fatalf("unexpected message: %+v", got)
	}
	if len(got.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(got.Content))
	}
	if got.Content[0].GetText().Text != "Hello world" {
		t.Fatalf("unexpected content: %s", got.Content[0].GetText().Text)
	}
}

func TestListMessages_Order(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})

	for i, text := range []string{"first", "second", "third"} {
		db.InsertMessage(&pb.Message{
			Id: text, ThreadId: "t1", Role: pb.Role_ROLE_USER,
			Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
			Position: int64(i), CreatedAt: timestamppb.Now(),
		})
	}

	msgs, err := db.ListMessages("t1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// Should be ordered by position
	if msgs[0].Id != "first" || msgs[1].Id != "second" || msgs[2].Id != "third" {
		t.Fatal("messages should be in position order")
	}
}

func TestThreadCorpus(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})
	db.InsertMessage(&pb.Message{
		Id: "m1", ThreadId: "t1", Role: pb.Role_ROLE_USER,
		Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hello"}}}},
		Position: 0, CreatedAt: timestamppb.Now(),
	})

	corpus, err := db.ThreadCorpus("t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus) != 1 {
		t.Fatalf("expected 1 message in corpus, got %d", len(corpus))
	}
}

func TestMessageCount(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})

	if count := db.MessageCount("t1"); count != 0 {
		t.Fatalf("expected 0 messages, got %d", count)
	}

	for i := 0; i < 5; i++ {
		db.InsertMessage(&pb.Message{
			Id: "m" + string(rune('0'+i)), ThreadId: "t1", Role: pb.Role_ROLE_USER,
			Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "msg"}}}},
			Position: int64(i), CreatedAt: timestamppb.Now(),
		})
	}

	if count := db.MessageCount("t1"); count != 5 {
		t.Fatalf("expected 5 messages, got %d", count)
	}
}

// --- Edge tests ---

func TestInsertAndLoadEdges(t *testing.T) {
	db := testDB(t)

	edge := &pb.Edge{
		FromMessageId:     "m0",
		ToMessageId:       "m1",
		Score:             0.85,
		Source:            pb.EdgeSource_EDGE_SOURCE_CROSS_ENCODER,
		CrossEncoderScore: 0.8,
		QudWeight:         0.0,
		TemporalProximity: 0.5,
		DetectedAt:        timestamppb.Now(),
		FromThreadId:      "t1",
		ToThreadId:        "t1",
	}
	if err := db.InsertEdge(edge); err != nil {
		t.Fatal(err)
	}

	edges, err := db.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	got := edges[0]
	if got.FromMessageId != "m0" || got.ToMessageId != "m1" {
		t.Fatal("wrong edge endpoints")
	}
	if got.Score != 0.85 {
		t.Fatalf("expected score 0.85, got %f", got.Score)
	}
	if got.CrossEncoderScore != 0.8 {
		t.Fatalf("expected CE score 0.8, got %f", got.CrossEncoderScore)
	}
}

func TestDeleteEdgesForThread(t *testing.T) {
	db := testDB(t)

	// Edge in t1
	db.InsertEdge(&pb.Edge{
		FromMessageId: "m0", ToMessageId: "m1", Score: 0.8,
		DetectedAt: timestamppb.Now(), FromThreadId: "t1", ToThreadId: "t1",
	})
	// Edge in t2
	db.InsertEdge(&pb.Edge{
		FromMessageId: "m2", ToMessageId: "m3", Score: 0.7,
		DetectedAt: timestamppb.Now(), FromThreadId: "t2", ToThreadId: "t2",
	})

	db.DeleteEdgesForThread("t1")

	edges, _ := db.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge after delete, got %d", len(edges))
	}
	if edges[0].FromThreadId != "t2" {
		t.Fatal("wrong edge survived delete")
	}
}

// --- Backfill test ---

func TestBackfillThreadNames(t *testing.T) {
	db := testDB(t)

	// Thread with default name
	db.CreateThread(&pb.Thread{Id: "t1", Name: "New Thread", CreatedAt: timestamppb.Now()})
	// Thread with real name
	db.CreateThread(&pb.Thread{Id: "t2", Name: "My Real Thread", CreatedAt: timestamppb.Now()})

	// Add a message to t1
	db.InsertMessage(&pb.Message{
		Id: "m1", ThreadId: "t1", Role: pb.Role_ROLE_USER,
		Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "What is quantum computing?"}}}},
		Position: 0, CreatedAt: timestamppb.Now(),
	})

	count := db.BackfillThreadNames()
	if count != 1 {
		t.Fatalf("expected 1 backfilled, got %d", count)
	}

	got, _ := db.GetThread("t1")
	if got.Name != "What is quantum computing?" {
		t.Fatalf("expected backfilled name, got %q", got.Name)
	}

	// t2 should be unchanged
	got2, _ := db.GetThread("t2")
	if got2.Name != "My Real Thread" {
		t.Fatalf("named thread should be unchanged, got %q", got2.Name)
	}
}

// --- DB creation test ---

func TestOpen_CreatesDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// File should exist
	if _, err := os.Stat(filepath.Join(dir, "spidey.db")); os.IsNotExist(err) {
		t.Fatal("database file should exist")
	}
}

func TestCascadeDelete(t *testing.T) {
	db := testDB(t)

	db.CreateThread(&pb.Thread{Id: "t1", Name: "Test", CreatedAt: timestamppb.Now()})
	db.InsertMessage(&pb.Message{
		Id: "m1", ThreadId: "t1", Role: pb.Role_ROLE_USER,
		Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hello"}}}},
		Position: 0, CreatedAt: timestamppb.Now(),
	})

	// Delete thread — should cascade to messages
	db.DeleteThread("t1")

	_, err := db.GetMessage("m1")
	if err == nil {
		t.Fatal("message should be cascade-deleted with thread")
	}
}

// TestAgentState_NarrowHelpersPreserveFields verifies that the narrow
// update helpers (SetAgentStatus, SetAgentRoundCount, SetAgentStatusAndMode)
// do NOT wipe the other columns. Regression guard against the rounds-93/97
// partial-UPSERT bug where SaveAgentState{Status: x} would reset mode/
// roundCount/startedAt/durationLimit to zero values.
func TestAgentState_NarrowHelpersPreserveFields(t *testing.T) {
	db := testDB(t)
	db.CreateThread(&pb.Thread{Id: "t-agent", Name: "T", CreatedAt: timestamppb.Now()})

	started := timestamppb.Now().AsTime()
	full := &AgentState{
		ThreadID:      "t-agent",
		Status:        AgentStatusRunning,
		Mode:          AgentModeAutonomous,
		RoundCount:    5,
		StartedAt:     &started,
		DurationLimit: "1h",
	}
	if err := db.SaveAgentState(full); err != nil {
		t.Fatalf("SaveAgentState full: %v", err)
	}

	// SetAgentStatus only touches Status.
	if err := db.SetAgentStatus("t-agent", AgentStatusPaused); err != nil {
		t.Fatalf("SetAgentStatus: %v", err)
	}
	got, err := db.GetAgentState("t-agent")
	if err != nil {
		t.Fatalf("GetAgentState after SetAgentStatus: %v", err)
	}
	if got.Status != AgentStatusPaused {
		t.Errorf("Status = %v, want Paused", got.Status)
	}
	if got.Mode != AgentModeAutonomous {
		t.Errorf("Mode wiped by SetAgentStatus: got %v, want Autonomous", got.Mode)
	}
	if got.RoundCount != 5 {
		t.Errorf("RoundCount wiped by SetAgentStatus: got %d, want 5", got.RoundCount)
	}
	if got.DurationLimit != "1h" {
		t.Errorf("DurationLimit wiped by SetAgentStatus: got %q, want 1h", got.DurationLimit)
	}
	if got.StartedAt == nil {
		t.Errorf("StartedAt wiped by SetAgentStatus")
	}

	// SetAgentRoundCount only touches RoundCount.
	if err := db.SetAgentRoundCount("t-agent", 42); err != nil {
		t.Fatalf("SetAgentRoundCount: %v", err)
	}
	got, _ = db.GetAgentState("t-agent")
	if got.RoundCount != 42 {
		t.Errorf("RoundCount = %d, want 42", got.RoundCount)
	}
	if got.Status != AgentStatusPaused {
		t.Errorf("Status wiped by SetAgentRoundCount: got %v, want Paused", got.Status)
	}
	if got.DurationLimit != "1h" {
		t.Errorf("DurationLimit wiped by SetAgentRoundCount: got %q, want 1h", got.DurationLimit)
	}

	// SetAgentStatusAndMode touches both, nothing else.
	if err := db.SetAgentStatusAndMode("t-agent", AgentStatusRunning, AgentModePlan); err != nil {
		t.Fatalf("SetAgentStatusAndMode: %v", err)
	}
	got, _ = db.GetAgentState("t-agent")
	if got.Status != AgentStatusRunning {
		t.Errorf("Status = %v, want Running", got.Status)
	}
	if got.Mode != AgentModePlan {
		t.Errorf("Mode = %v, want Plan", got.Mode)
	}
	if got.RoundCount != 42 {
		t.Errorf("RoundCount wiped by SetAgentStatusAndMode: got %d, want 42", got.RoundCount)
	}
	if got.DurationLimit != "1h" {
		t.Errorf("DurationLimit wiped by SetAgentStatusAndMode: got %q, want 1h", got.DurationLimit)
	}
}

// TestAgentState_SaveAgentStateIsFullUPSERT documents that SaveAgentState
// writes every field — it does NOT preserve unset ones. Callers that want
// to change a single column must use the narrow helpers instead. This test
// locks in the contract so future changes to SaveAgentState don't silently
// drift into partial-write behavior that callers might rely on.
func TestAgentState_SaveAgentStateIsFullUPSERT(t *testing.T) {
	db := testDB(t)
	db.CreateThread(&pb.Thread{Id: "t-full", Name: "T", CreatedAt: timestamppb.Now()})

	started := timestamppb.Now().AsTime()
	if err := db.SaveAgentState(&AgentState{
		ThreadID: "t-full", Status: AgentStatusRunning, Mode: AgentModeAutonomous,
		RoundCount: 5, StartedAt: &started, DurationLimit: "1h",
	}); err != nil {
		t.Fatalf("SaveAgentState: %v", err)
	}

	// Partial write — other fields SHOULD be zeroed by design.
	if err := db.SaveAgentState(&AgentState{
		ThreadID: "t-full", Status: AgentStatusPaused,
	}); err != nil {
		t.Fatalf("SaveAgentState partial: %v", err)
	}
	got, _ := db.GetAgentState("t-full")
	if got.Mode != AgentModeNormal {
		t.Errorf("Mode = %v after partial save, want Normal (zero) — if this changed, fix callers", got.Mode)
	}
	if got.RoundCount != 0 {
		t.Errorf("RoundCount = %d after partial save, want 0", got.RoundCount)
	}
	if got.DurationLimit != "" {
		t.Errorf("DurationLimit = %q after partial save, want empty", got.DurationLimit)
	}
}
