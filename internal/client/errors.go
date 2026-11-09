package client

import "fmt"

// wrapOp prefixes a transport-level error with the operation that failed.
// API errors already carry the operation.
func wrapOp(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
