package rrc

import "errors"

var (
	ErrClassifierFailed = errors.New("classifier returned error during scoring")
	ErrCompleterFailed  = errors.New("small model failed during QUD extraction")
	ErrMessageNotFound  = errors.New("message ID not found in data structures")
	ErrThreadNotFound   = errors.New("thread ID not found in data structures")
)
