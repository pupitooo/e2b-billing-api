package billing

const (
	maxIdempotencyKeyBytes = 256
	firstVisibleASCII      = '!'
	lastVisibleASCII       = '~'
)

// ValidateIdempotencyKey preserves opaque JSON operation keys and enforces the
// same visible ASCII and byte boundaries as durable API operation history.
func ValidateIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > maxIdempotencyKeyBytes {
		return invalidIdempotencyKey()
	}

	for _, character := range key {
		if character < firstVisibleASCII || character > lastVisibleASCII {
			return invalidIdempotencyKey()
		}
	}

	return nil
}

func invalidIdempotencyKey() error {
	return &ValidationError{Field: "idempotency_key", Message: "Supply idempotency_key as 1 through 256 visible ASCII characters without whitespace."}
}
