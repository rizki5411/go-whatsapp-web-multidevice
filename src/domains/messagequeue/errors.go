package messagequeue

import "errors"

// ErrQueueRowNotPending is returned when a row exists but is no longer pending,
// so it can no longer be cancelled.
var ErrQueueRowNotPending = errors.New("queue row is not pending")
