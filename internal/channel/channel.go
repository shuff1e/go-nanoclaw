// Package channel provides communication channel abstractions.
package channel

import "context"

// Channel is the interface for bidirectional communication.
type Channel interface {
	Start(ctx context.Context) error
	Stop() error
	Send(message string) error
}
