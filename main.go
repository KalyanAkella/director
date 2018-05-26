package main

import (
	"go-broadcaster/broadcaster"
	"log"
	"net/url"
)

func asUrl(urlString string) *url.URL {
	aUrl, err := url.Parse(urlString)
	if err != nil {
		panic(err)
	} else {
		return aUrl
	}
}

func main() {
	config := broadcaster.BroadcastConfig{
		Backends: map[string]*url.URL{
			"1": asUrl("http://localhost:9091"),
			"2": asUrl("http://localhost:9092"),
		},
		Options: map[broadcaster.BroadcastOption]string{
			broadcaster.PORT:                     "9090",
			broadcaster.PRIMARY:                  "1",
			broadcaster.RESPONSE_TIMEOUT_IN_SECS: "10",
		},
	}
	log.Fatal(broadcaster.ServeOnHTTP(&config))
}
