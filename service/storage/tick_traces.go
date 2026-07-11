package storage

import (
	"database/sql"
	"time"
)

// TickTrace is one decomposed-latency record for a single SendMessage
// invocation. Field naming mirrors the tick_traces table columns
// 1:1. All durations are milliseconds. The five stage timings let a
// caller answer "where did the wall clock go" without parsing logs.
//
// Stage definitions:
//
//   - RRCPrerequisiteSelectionMs: Engine.SelectPrerequisites (Local Context
//     serialization + KNN +
//     reranker + edge writes)
//   - SelectMs: Engine.Select (graph walk + transitive reduction)
//   - AssembleMs: Engine.Assemble post-Select (MMR + budget shed +
//     token estimation; excludes nested SelectPrerequisites/Select already
//     accounted for above)
//   - CompleteMs: time-to-first-event on the upstream completer
//     (network round-trip to the model)
//   - StreamMs: total stream duration (CompleteMs + interleaved
//     streaming events + per-event processing)
//   - PersistMs: sum of per-InsertMessage durations inside
//     processEvents (strict subset of StreamMs; isolates pure SQLite
//     write cost)
//   - TotalMs: wall clock around the whole SendMessage
//
// Diagnostic reading: high CompleteMs → model slow. High
// (StreamMs - CompleteMs) but low PersistMs → streaming network slow.
// High PersistMs → SQLite contention.
type TickTrace struct {
	ID                         int64
	ThreadID                   string
	Round                      int
	CreatedAt                  time.Time
	RRCPrerequisiteSelectionMs int64
	SelectMs                   int64
	AssembleMs                 int64
	CompleteMs                 int64
	StreamMs                   int64
	PersistMs                  int64
	TotalMs                    int64
	CompleterModel             string
	CorpusSize                 int
	SelectedCount              int
	AssembledTokensEst         int
	// The tick's last usage-bearing model call, as one atomic triple:
	// the assembler's counter-unit prediction for that call's wire and
	// the provider's reported prompt/completion totals. All zero when
	// no call reported usage. UsagePromptTokens/UsagePredictedTokens
	// is the grounding ratio the token-scale learner feeds on.
	UsagePredictedTokens  int
	UsagePromptTokens     int
	UsageCompletionTokens int
	Errored               bool
	ErrorMsg              string
}

// InsertTickTrace appends one trace row. Telemetry must never block
// the agent loop — callers log+discard errors rather than propagating.
func (d *DB) InsertTickTrace(t *TickTrace) error {
	errored := 0
	if t.Errored {
		errored = 1
	}
	var errMsg sql.NullString
	if t.ErrorMsg != "" {
		errMsg = sql.NullString{String: t.ErrorMsg, Valid: true}
	}
	res, err := d.Exec(
		`INSERT INTO tick_traces (
			thread_id, round,
			t_rrc_prerequisite_selection_ms, t_select_ms, t_assemble_ms,
			t_complete_ms, t_stream_ms, t_persist_ms, t_total_ms,
			completer_model, corpus_size, selected_count,
			assembled_tokens_est,
			usage_predicted_tokens, usage_prompt_tokens, usage_completion_tokens,
			errored, error_msg
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ThreadID, t.Round,
		t.RRCPrerequisiteSelectionMs, t.SelectMs, t.AssembleMs,
		t.CompleteMs, t.StreamMs, t.PersistMs, t.TotalMs,
		t.CompleterModel, t.CorpusSize, t.SelectedCount,
		t.AssembledTokensEst,
		t.UsagePredictedTokens, t.UsagePromptTokens, t.UsageCompletionTokens,
		errored, errMsg,
	)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		t.ID = id
	}
	return nil
}

// ListTickTraces returns the most recent `limit` traces for a thread,
// newest first. limit <= 0 means "all."
func (d *DB) ListTickTraces(threadID string, limit int) ([]*TickTrace, error) {
	q := `SELECT id, thread_id, round, created_at,
		t_rrc_prerequisite_selection_ms, t_select_ms, t_assemble_ms,
		t_complete_ms, t_stream_ms, t_persist_ms, t_total_ms,
		completer_model, corpus_size, selected_count,
		assembled_tokens_est,
		usage_predicted_tokens, usage_prompt_tokens, usage_completion_tokens,
		errored, error_msg
		FROM tick_traces
		WHERE thread_id = ?
		ORDER BY id DESC`
	args := []any{threadID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*TickTrace
	for rows.Next() {
		t := &TickTrace{}
		var model sql.NullString
		var errMsg sql.NullString
		var errored int
		if err := rows.Scan(
			&t.ID, &t.ThreadID, &t.Round, &t.CreatedAt,
			&t.RRCPrerequisiteSelectionMs, &t.SelectMs, &t.AssembleMs,
			&t.CompleteMs, &t.StreamMs, &t.PersistMs, &t.TotalMs,
			&model, &t.CorpusSize, &t.SelectedCount,
			&t.AssembledTokensEst,
			&t.UsagePredictedTokens, &t.UsagePromptTokens, &t.UsageCompletionTokens,
			&errored, &errMsg,
		); err != nil {
			return nil, err
		}
		if model.Valid {
			t.CompleterModel = model.String
		}
		if errMsg.Valid {
			t.ErrorMsg = errMsg.String
		}
		t.Errored = errored != 0
		out = append(out, t)
	}
	return out, rows.Err()
}
