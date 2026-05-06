package stun

import "time"

// timeBeforeNow returns a time guaranteed to be in the past, used to
// kick a blocking Read out of its deadline.
func timeBeforeNow() time.Time { return time.Unix(1, 0) }
