package app

// nilDependency keeps the RTW adapters' validation semantics identical after
// the shared async app package was split by service ownership.
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
