package billing

import "errors"

var (
	ErrNotFound = errors.New("billing resource not found")
	ErrConflict = errors.New("operation conflicts with financial history")
)
