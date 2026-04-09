package charging

import "fmt"

const (
	StatePending  = "pending"
	StateStarting = "starting"
	StateCharging = "charging"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateFailed   = "failed"
)

var allowedTransitions = map[string]map[string]struct{}{
	StatePending: {
		StateStarting: {},
		StateFailed:   {},
	},
	StateStarting: {
		StateCharging: {},
		StateStopping: {},
		StateFailed:   {},
	},
	StateCharging: {
		StateStopping: {},
		StateStopped:  {},
		StateFailed:   {},
	},
	StateStopping: {
		StateCharging: {},
		StateStopped:  {},
		StateFailed:   {},
	},
}

func CanTransition(from, to string) bool {
	if from == to {
		return true
	}
	targets, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	_, ok = targets[to]
	return ok
}

func ValidateTransition(from, to string) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid state transition: %s -> %s", from, to)
	}
	return nil
}
