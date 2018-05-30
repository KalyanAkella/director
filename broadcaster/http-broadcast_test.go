package broadcaster

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newListener(endpoint string) net.Listener {
	if l, err := net.Listen("tcp", endpoint); err != nil {
		panic(err)
	} else {
		return l
	}
}

func newBroadcastServer(handler http.HandlerFunc) *httptest.Server {
	aServer := httptest.NewUnstartedServer(handler)
	aServer.Listener = newListener(fmt.Sprintf("localhost:%s", BroadcastServerPort))
	aServer.Start()
	return aServer
}

func newServer(tag, endpoint string, res_chan chan<- string) *httptest.Server {
	aServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := fmt.Sprintf("%s Got Request", tag)
		res_chan <- response
		fmt.Fprint(w, response)
	}))
	aServer.Listener = newListener(endpoint)
	aServer.Start()
	return aServer
}

const (
	PrimaryTag          = "B2"
	BroadcastServerPort = "9090"
	NumRequests         = 10
)

func readTag(response string) string {
	var tag string
	if _, err := fmt.Sscanf(response, "%s", &tag); err != nil {
		panic(err)
	}
	return tag
}

func httpGet(url string) string {
	if res, err := http.Get(url); err != nil {
		panic(err)
	} else {
		defer res.Body.Close()
		if res_bytes, err := ioutil.ReadAll(res.Body); err != nil {
			panic(err)
		} else {
			return string(res_bytes)
		}
	}
}

var broadcast_server *httptest.Server
var (
	backendServers = map[string]string{
		"B1":       "localhost:9091",
		PrimaryTag: "localhost:9092",
		"B3":       "localhost:9093",
	}
	backends = make([]*httptest.Server, len(backendServers))
	res_chan = make(chan string, len(backendServers))
)

func startBackendServers() {
	i := 0
	for t, e := range backendServers {
		backends[i] = newServer(t, e, res_chan)
		i++
	}
}

func startBroadcastServer() {
	servers := make(map[string]string, len(backendServers))
	for t, e := range backendServers {
		servers[t] = fmt.Sprintf("http://%s", e)
	}
	if broadcaster, err := NewBroadcaster(&BroadcastConfig{
		Backends: servers,
		Options: map[BroadcastOption]string{
			PORT:                     BroadcastServerPort,
			PRIMARY:                  PrimaryTag,
			RESPONSE_TIMEOUT_IN_SECS: "10",
		},
	}); err != nil {
		log.Fatal(err)
	} else {
		broadcast_server = newBroadcastServer(broadcaster.Handler)
	}
}

func setup() {
	startBackendServers()
	startBroadcastServer()
}

func teardown() {
	broadcast_server.Close()
	for _, backend := range backends {
		backend.Close()
	}
}

func testSingleBroadcastRequest(tb testing.TB) {
	responded := make(map[string]bool, len(backendServers))
	broadcast_res := httpGet("http://localhost:9090")
	if primary_tag := readTag(broadcast_res); primary_tag != PrimaryTag {
		tb.Errorf("Expected primary tag %s, Actual primary tag %s. Broadcast Response %s", PrimaryTag, primary_tag, broadcast_res)
	}
	for range backendServers {
		select {
		case msg := <-res_chan:
			responded[readTag(msg)] = true
		default:
		}
	}
	for k, v := range responded {
		if !v {
			tb.Errorf("No response from server with tag: %s", k)
		}
	}
}

func BenchmarkHTTPBroadcast(b *testing.B) {
	setup()
	defer teardown()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			testSingleBroadcastRequest(b)
		}
	})
}

func TestHTTPBroadcast(t *testing.T) {
	setup()
	defer teardown()
	for i := 1; i <= NumRequests; i++ {
		testSingleBroadcastRequest(t)
	}
}
