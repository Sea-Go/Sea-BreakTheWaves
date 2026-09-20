package app

// nilDependency preserves the adapter validation semantics formerly shared with
// Async application workers.
func nilDependency(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case interface{ IsNil() bool }:
		return typed.IsNil()
	default:
		return false
	}
}
