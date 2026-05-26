package rrc

import "errors"

var (
	ErrClassifierFailed      = errors.New("classifier returned error during scoring")
	ErrClassifierUnavailable = errors.New("no classifier configured — RRC cannot score edges")
	ErrMessageNotFound       = errors.New("message ID not found in data structures")
	ErrThreadNotFound        = errors.New("thread ID not found in data structures")
)
