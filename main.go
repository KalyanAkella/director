package main

import (
	"go-broadcaster/broadcaster"
	"log"
)

func main() {
	if broadcaster, err := broadcaster.NewBroadcaster(&broadcaster.BroadcastConfig{
		Backends: map[string]string{
			"1": "http://localhost:9091",
			"2": "http://localhost:9092",
		},
		Options: &broadcaster.BroadcastOptions{
			Port:            8080,
			PrimaryEndpoint: "1",
			LogLevel:        broadcaster.INFO,
		},
	}); err != nil {
		log.Fatal(err)
	} else {
		log.Fatal(broadcaster.ListenAndServe())
	}
}
