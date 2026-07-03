package rrc

import "errors"

var (
	ErrScorerFailed      = errors.New("scorer returned error during scoring")
	ErrScorerUnavailable = errors.New("no scorer configured — RRC cannot score edges")
	ErrMessageNotFound   = errors.New("message ID not found in data structures")
	ErrThreadNotFound    = errors.New("thread ID not found in data structures")
)
