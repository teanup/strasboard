// Package temperature defines the payload types for indoor sensor readings.
package temperature

import (
	"slices"
	"time"
)

// ResourceName is the store resource holding temperature readings.
const ResourceName = "temperature"

// CurrentID is the derived item ID exposing the aggregated current reading.
const CurrentID = "current"

// Window is how many recent readings are aggregated into the current value.
const Window = 5

// Reading is a single sensor measurement.
type Reading struct {
	Time        time.Time `json:"time"`
	Temperature float64   `json:"temperature"`
	Humidity    float64   `json:"humidity"`
	Location    string    `json:"location"`
}

// Current is the aggregated value exposed at GET /api/v1/temperature/current.
type Current struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	Location    string  `json:"location"`
}

// Aggregate computes the current value from the most recent readings.
// It returns false if there are no readings.
func Aggregate(readings []Reading) (Current, bool) {
	if len(readings) == 0 {
		return Current{}, false
	}
	if len(readings) > Window {
		readings = readings[len(readings)-Window:]
	}

	temps := make([]float64, len(readings))
	hums := make([]float64, len(readings))
	for i, r := range readings {
		temps[i] = r.Temperature
		hums[i] = r.Humidity
	}

	return Current{
		Temperature: median(temps),
		Humidity:    median(hums),
		Location:    readings[len(readings)-1].Location,
	}, true
}

// median returns the median of a float64 slice. The slice must not be empty.
func median(vals []float64) float64 {
	sorted := slices.Clone(vals)
	slices.Sort(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
