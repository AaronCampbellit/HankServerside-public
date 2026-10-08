//go:build !unix

package operations

import (
	"errors"
	"os"
)

// Unsupported platforms must not advertise durable receipt execution until
// both exclusive locking and directory durability have native implementations.
func lockJournal(*os.File) error {
	return errors.New("durable operation journal unavailable on this platform")
}
func unlockJournal(*os.File) error { return nil }
