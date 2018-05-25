package main

import (
	"go-broadcaster/broadcaster"
	"log"
	"net/http"
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
	backends := map[string]*url.URL{
		"1": asUrl("http://localhost:9091"),
		"2": asUrl("http://localhost:9092"),
	}
	if broadcast_handler, err := broadcaster.BroadcastHTTPHandler(&broadcaster.BroadcastConfig{
		Backends: backends,
		Options: map[broadcaster.BroadcastOption]string{
			broadcaster.PORT:                     "9090",
			broadcaster.PRIMARY:                  "1",
			broadcaster.RESPONSE_TIMEOUT_IN_SECS: "10",
		},
	}); err != nil {
		panic(err)
	} else {
		log.Fatal(http.ListenAndServe(":9090", broadcast_handler))
	}
}
