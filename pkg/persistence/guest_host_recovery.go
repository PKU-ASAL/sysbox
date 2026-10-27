package persistence

// RecoverGuestExecutionHostCompletion validates only the durable completion
// proof and republishes the corresponding stopped authority. It never needs a
// worker command or the original signed receipt.
func RecoverGuestExecutionHostCompletion(id string, raw, authority []byte) (*GuestExecutionMutation, error) {
	return PrepareGuestExecutionStopRecovery(id, raw, authority, nil)
}
