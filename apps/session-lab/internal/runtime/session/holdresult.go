package session

// holdResult replaces the two unnamed booleans that hold() used to return. That
// signature is how 30 successful sessions were recorded as failures: the call
// site bound the wrong bool and nothing in the type said which was which.
//
// Named fields cannot be swapped by accident, and Reason carries why a hold
// ended, so a report can say "the run was cancelled" instead of "timeout".
type holdResult struct {
	Reading Reading
	// Sampled reports whether any resource reading was collected.
	Sampled bool
	// Completed reports whether the hold ran its full duration.
	Completed bool
	// Reason explains an early end: "cancelled", "deadline", "no-instance".
	Reason string
}

// interrupted reports whether the hold ended before its duration.
func (h holdResult) interrupted() bool { return !h.Completed }
