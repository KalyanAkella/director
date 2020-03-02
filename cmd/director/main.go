package main

import (
	"log"

	"github.com/KalyanAkella/director/internal/proxy"
)

func main() {
	if director, err := proxy.NewDirector(&proxy.ProxyConfig{
		Backends: map[string]string{
			"1": "http://localhost:9091",
			"2": "http://localhost:9092",
		},
		Options: &proxy.ProxyOptions{
			Port:            8080,
			PrimaryEndpoint: "1",
			LogLevel:        proxy.INFO,
		},
	}); err != nil {
		log.Fatal(err)
	} else {
		log.Fatal(director.ListenAndServe())
	}
}
