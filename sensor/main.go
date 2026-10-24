// Command sensor reads a DHT22 sensor and posts readings to the StrasBoard API.
package main

import (
	"log"
	"time"

	"strasboard/sensor/client"
	"strasboard/sensor/config"
	"strasboard/sensor/dht22"
)

var version = "dev"

func main() {
	log.Printf("StrasBoard sensor %s", version)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	sensor, err := dht22.New(cfg.Pin)
	if err != nil {
		log.Fatalf("sensor init: %v", err)
	}

	api := client.New(cfg.ServerURL, cfg.AuthToken)
	interval := cfg.ReadInterval()

	log.Printf("polling DHT22 every %v, posting to %s", interval, cfg.ServerURL)

	for {
		r, err := sensor.ReadWithRetry(cfg.MaxRetries)
		if err != nil {
			log.Printf("read: %v", err)
			time.Sleep(interval)
			continue
		}

		reading := client.Reading{
			Temperature: r.Temperature,
			Humidity:    r.Humidity,
			Location:    cfg.Location,
		}

		if err := api.Post(reading); err != nil {
			log.Printf("post: %v", err)
		} else if cfg.Debug {
			log.Printf("sent: %.1f °C, %.1f %%, %s", reading.Temperature, reading.Humidity, reading.Location)
		}

		time.Sleep(interval)
	}
}
