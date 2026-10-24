// Package dht22 reads temperature and humidity from a DHT22 (AM2302) sensor
// connected to a single GPIO data pin via periph.io.
//
// Derived from go-dht (https://github.com/MichaelS11/go-dht), MIT licensed:
//
//	MIT License
//
//	Copyright (c) 2018 MichaelS11
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy
//	of this software and associated documentation files (the "Software"), to deal
//	in the Software without restriction, including without limitation the rights
//	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//	copies of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all
//	copies or substantial portions of the Software.
package dht22

import (
	"fmt"
	"runtime/debug"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

// Protocol timing constants per the AM2302 datasheet.
const (
	startLowDuration = 1 * time.Millisecond  // host pulls LOW for >= 1 ms
	bitThreshold     = 30 * time.Microsecond // HIGH > 30 µs is a 1 bit
	maxHighPulse     = 90 * time.Microsecond
	maxLowPulse      = 70 * time.Microsecond
	minLowPulse      = 35 * time.Microsecond
	transitionCount  = 84 // 2 ack + 40 bits * 2 edges
	minReadInterval  = 2 * time.Second
)

// Reading holds a single sensor measurement.
type Reading struct {
	Humidity    float64 // relative humidity, 0% - 100%
	Temperature float64 // room temperature, -40°C - 80°C
}

// Sensor controls a DHT22 on a single GPIO pin.
type Sensor struct {
	pin      gpio.PinIO
	lastRead time.Time
}

// New initializes the periph.io host and returns a Sensor for pinName.
// The pin is set HIGH (idle) before returning.
func New(pinName string) (*Sensor, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("dht22: host init: %w", err)
	}
	pin := gpioreg.ByName(pinName)
	if pin == nil {
		return nil, fmt.Errorf("dht22: pin %q not found", pinName)
	}
	if err := pin.Out(gpio.High); err != nil {
		return nil, fmt.Errorf("dht22: set pin high: %w", err)
	}
	return &Sensor{
		pin:      pin,
		lastRead: time.Now().Add(-time.Second), // 1 s warm-up
	}, nil
}

// Read performs one sensor read. It enforces the 2 s minimum interval
// required by the DHT22 datasheet.
func (s *Sensor) Read() (Reading, error) {
	if d := minReadInterval - time.Since(s.lastRead); d > 0 {
		time.Sleep(d)
	}

	bits, err := s.sample()
	if err != nil {
		return Reading{}, err
	}
	return parse(bits)
}

// ReadWithRetry calls Read up to n times, returning the first success.
func (s *Sensor) ReadWithRetry(n int) (Reading, error) {
	var err error
	for i := 0; i < n; i++ {
		var r Reading
		if r, err = s.Read(); err == nil {
			return r, nil
		}
	}
	return Reading{}, fmt.Errorf("dht22: %d attempts failed: %w", n, err)
}

// sample runs the full one-wire exchange and returns 40 data bits.
// GC is disabled during the timing-critical capture phase.
func (s *Sensor) sample() ([]int, error) {
	s.lastRead = time.Now()

	gcPrev := debug.SetGCPercent(-1)

	if err := s.startSignal(); err != nil {
		debug.SetGCPercent(gcPrev)
		s.pin.Out(gpio.High)
		return nil, err
	}

	levels, durations := s.capture()

	debug.SetGCPercent(gcPrev)
	s.pin.Out(gpio.High)

	return decode(levels, durations)
}

// startSignal sends the host start pulse: pull LOW for >= 1 ms, then
// release the line with an internal pull-up.
func (s *Sensor) startSignal() error {
	if err := s.pin.Out(gpio.Low); err != nil {
		return fmt.Errorf("dht22: start low: %w", err)
	}
	time.Sleep(startLowDuration)
	if err := s.pin.In(gpio.PullUp, gpio.NoEdge); err != nil {
		return fmt.Errorf("dht22: release pin: %w", err)
	}
	return nil
}

// capture busy-reads transitionCount level changes from the data line.
func (s *Sensor) capture() ([]gpio.Level, []time.Duration) {
	levels := make([]gpio.Level, 0, transitionCount)
	durations := make([]time.Duration, 0, transitionCount)

	prev := s.pin.Read()
	for range transitionCount {
		start := time.Now()
		cur := prev
		for cur == prev && time.Since(start) < time.Millisecond {
			cur = s.pin.Read()
		}
		durations = append(durations, time.Since(start))
		levels = append(levels, prev)
		prev = cur
	}
	return levels, durations
}

// decode extracts 40 data bits from raw level transitions.
func decode(levels []gpio.Level, durations []time.Duration) ([]int, error) {
	// Find the last LOW transition, marks the end of the 40th bit.
	end := -1
	for i := len(levels) - 1; i >= 80; i-- {
		if levels[i] == gpio.Low {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("dht22: insufficient transitions")
	}

	base := end - 79
	bits := make([]int, 40)

	for i := range 40 {
		hi := base + i*2
		lo := hi + 1

		if levels[hi] != gpio.High {
			return nil, fmt.Errorf("dht22: expected high at transition %d", hi)
		}
		if levels[lo] != gpio.Low {
			return nil, fmt.Errorf("dht22: expected low at transition %d", lo)
		}
		if durations[hi] > maxHighPulse {
			return nil, fmt.Errorf("dht22: high pulse too long (%v)", durations[hi])
		}
		if durations[lo] < minLowPulse || durations[lo] > maxLowPulse {
			return nil, fmt.Errorf("dht22: low pulse out of range (%v)", durations[lo])
		}
		if durations[hi] > bitThreshold {
			bits[i] = 1
		}
	}
	return bits, nil
}

// parse converts 40 bits into a Reading after verifying the checksum.
//
// Bit layout (MSB first):
//
//	[0 - 15]  humidity * 10    (unsigned)
//	[16 - 31] temperature * 10 (bit 15 = sign)
//	[32 - 39] checksum         (sum of preceding 4 bytes)
func parse(bits []int) (Reading, error) {
	var b [5]byte
	for i := range 5 {
		for j := range 8 {
			b[i] = b[i]<<1 | byte(bits[i*8+j])
		}
	}

	if sum := b[0] + b[1] + b[2] + b[3]; sum != b[4] {
		return Reading{}, fmt.Errorf("dht22: checksum mismatch (got %#02x, want %#02x)", sum, b[4])
	}

	hum := int(b[0])<<8 | int(b[1])
	if hum > 1000 {
		return Reading{}, fmt.Errorf("dht22: humidity out of range (%d)", hum)
	}

	temp := int(b[2])<<8 | int(b[3])
	if temp&0x8000 != 0 {
		temp = -(temp & 0x7FFF)
	}
	if temp < -400 || temp > 800 {
		return Reading{}, fmt.Errorf("dht22: temperature out of range (%d)", temp)
	}

	return Reading{
		Humidity:    float64(hum) / 10,
		Temperature: float64(temp) / 10,
	}, nil
}
