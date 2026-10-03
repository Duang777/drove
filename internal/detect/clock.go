package detect

import "time"

// Clock supplies observation timestamps without coupling decisions to wall time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now().UTC()
}

// SystemClock returns the production wall clock.
func SystemClock() Clock {
	return systemClock{}
}
