// Package temperature defines the payload type for indoor sensor readings and
// the retention policy applied to them.
//
// Readings are pushed by the sensor through the generic CRUD endpoint, so the
// policy is enforced by the HTTP layer rather than a background job.
package temperature

import "time"

// ResourceName is the store resource holding temperature readings.
const ResourceName = "temperature"

// Retention policy for stored readings.
const (
	// Window is how many recent readings are kept. Older readings are pruned
	// on every write so the resource never grows unbounded.
	Window = 5

	// MaxAge is how old the newest reading may be before the resource is
	// considered stale. A stale resource keeps its readings but is flagged
	// with an error, so clients know the sensor has gone quiet.
	MaxAge = 1 * time.Hour
)

// Reading is a single sensor measurement.
type Reading struct {
	Time        time.Time `json:"time"`
	Temperature float64   `json:"temperature"`
	Humidity    float64   `json:"humidity"`
	Location    string    `json:"location"`
}

// StaleError is the message reported when the newest reading is older than
// MaxAge.
const StaleError = "no recent reading"

// Stale reports whether the newest reading is older than MaxAge. It returns
// false when there are no readings (an empty resource is not "stale", it is
// simply empty).
func Stale(newest time.Time, now time.Time) bool {
	if newest.IsZero() {
		return false
	}
	return now.Sub(newest) > MaxAge
}
