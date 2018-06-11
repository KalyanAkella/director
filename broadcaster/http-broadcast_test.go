package broadcaster

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

/*

type TimingContext struct {
	Context interface{}
}

type MetricsReporter interface {
	Increment(tag string)
	Gauge(tag string, value interface{})
	Count(tag string, value interface{})
	StartTiming() *TimingContext
	EndTiming(tc *TimingContext, tag string)
}

*/

type Reporter struct {
	metrics map[string]uint64
	m       sync.Mutex
}

func (r *Reporter) Increment(tag string) {
	r.m.Lock()
	defer r.m.Unlock()
	r.metrics[tag]++
}

func (r *Reporter) Gauge(tag string, value interface{}) {
	r.m.Lock()
	defer r.m.Unlock()
	r.metrics[tag] = value.(uint64)
}

func (r *Reporter) Count(tag string, value interface{}) {
	r.m.Lock()
	defer r.m.Unlock()
	r.metrics[tag] += value.(uint64)
}

func (r *Reporter) StartTiming() *TimingContext { return nil }

func (r *Reporter) EndTiming(tc *TimingContext, tag string) {}

func (r *Reporter) Reset() {
	r.m.Lock()
	defer r.m.Unlock()
	r.metrics = make(map[string]uint64)
}

func newListener(endpoint string) net.Listener {
	if l, err := net.Listen("tcp", endpoint); err != nil {
		log.Fatal(err)
		return nil
	} else {
		return l
	}
}

func newBroadcastServer(handler http.HandlerFunc) *httptest.Server {
	aServer := httptest.NewUnstartedServer(handler)
	aServer.Listener = newListener(fmt.Sprintf("localhost:%d", BroadcastServerPort))
	aServer.Start()
	return aServer
}

func getHandler(tag string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			log.Fatal(err)
		}
		response := fmt.Sprintf("%s Got Request", tag)
		res_chan <- response
		fmt.Fprint(w, response)
	})
}

func postHandler(tag string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			log.Fatal(err)
		}
		log.Println(r.Form)
		response := fmt.Sprintf("%s Got Request", tag)
		res_chan <- response
		fmt.Fprint(w, response)
	})
}

func newPostServer(tag, endpoint string) *httptest.Server {
	aServer := httptest.NewUnstartedServer(postHandler(tag))
	aServer.Listener = newListener(endpoint)
	aServer.Start()
	return aServer
}

func newGetServer(tag, endpoint string) *httptest.Server {
	aServer := httptest.NewUnstartedServer(getHandler(tag))
	aServer.Listener = newListener(endpoint)
	aServer.Start()
	return aServer
}

const (
	PrimaryTag          = "B2"
	BroadcastServerPort = 9090
	NumRequests         = 10
)

func readTag(response string) string {
	var tag string
	if _, err := fmt.Sscanf(response, "%s", &tag); err != nil {
		log.Fatal(err)
	}
	return tag
}

func httpPost(httpUrl string, data map[string]string) (string, int) {
	values := url.Values{}
	for k, v := range data {
		values.Set(k, v)
	}
	if res, err := http.PostForm(httpUrl, values); err != nil {
		log.Fatal(err)
		return "", -1
	} else {
		defer res.Body.Close()
		if res_bytes, err := ioutil.ReadAll(res.Body); err != nil {
			log.Fatal(err)
			return "", -1
		} else {
			return string(res_bytes), res.StatusCode
		}
	}
}

func httpGet(url string) (string, int) {
	if res, err := http.Get(url); err != nil {
		log.Fatal(err)
		return "", -1
	} else {
		defer res.Body.Close()
		if res_bytes, err := ioutil.ReadAll(res.Body); err != nil {
			log.Fatal(err)
			return "", -1
		} else {
			return string(res_bytes), res.StatusCode
		}
	}
}

var broadcast_server *httptest.Server
var res_chan chan string
var backends map[string]*httptest.Server
var backendServers map[string]string
var reporter *Reporter

func startPostBackendServers() {
	backends = make(map[string]*httptest.Server)
	for t, e := range backendServers {
		backends[t] = newPostServer(t, e)
	}
}

func startGetBackendServers() {
	backends = make(map[string]*httptest.Server)
	for t, e := range backendServers {
		backends[t] = newGetServer(t, e)
	}
}

func startBroadcastServer() {
	servers := make(map[string]string, len(backendServers))
	for t, e := range backendServers {
		servers[t] = fmt.Sprintf("http://%s", e)
	}
	if broadcaster, err := NewBroadcaster(&BroadcastConfig{
		Backends: servers,
		Options: &BroadcastOptions{
			Port:            BroadcastServerPort,
			PrimaryEndpoint: PrimaryTag,
			LogLevel:        ERROR,
		},
	}); err != nil {
		log.Fatal(err)
	} else {
		reporter = &Reporter{metrics: make(map[string]uint64)}
		broadcaster.WithMetricsReporter(reporter)
		broadcast_server = newBroadcastServer(broadcaster.Handler)
	}
}

func setupForGet() {
	startGetBackendServers()
	startBroadcastServer()
}

func setupForPost() {
	startPostBackendServers()
	startBroadcastServer()
}

func teardown() {
	shutdownBackend(broadcast_server)
	for _, backend := range backends {
		shutdownBackend(backend)
	}
}

func shutdownBackend(backend *httptest.Server) {
	backend.CloseClientConnections()
	backend.Close()
}

func assertMetric(tb testing.TB, expected_value int, metric_name string) {
	if reporter.metrics[metric_name] != uint64(expected_value) {
		tb.Errorf("Metric name: %s. Expected: %d, Actual: %d", metric_name, expected_value, reporter.metrics[metric_name])
	}
}

func TestHTTPGetBroadcastWithFailureResponse(t *testing.T) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:9094"
	backendServers[PrimaryTag] = "localhost:9095"
	setupForGet()
	defer teardown()
	shutdownBackend(backends[PrimaryTag])
	_, status_code := httpGet("http://localhost:9090")
	assertStatusCode(t, status_code, http.StatusServiceUnavailable)
	assertMetric(t, 1, "primary.failure.count")
	assertMetric(t, 1, "broadcaster.request.count")
}

func TestHTTPPostBroadcastWithSuccessResponse(t *testing.T) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:8091"
	backendServers[PrimaryTag] = "localhost:8092"
	backendServers["B3"] = "localhost:8093"
	setupForPost()
	defer teardown()
	for i := 1; i <= NumRequests; i++ {
		res_chan = make(chan string, len(backendServers))
		broadcast_res, status_code := httpPost("http://localhost:9090", map[string]string{"K1": "V1"})
		assertStatusCode(t, status_code, http.StatusOK)
		assertForPrimaryResponse(t, broadcast_res)
		waitForSecondaryResponses(res_chan)
	}
	assertMetric(t, NumRequests, "primary.success.count")
	assertMetric(t, NumRequests, "broadcaster.request.count")
}

func TestHTTPGetBroadcastWithSuccessResponse(t *testing.T) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:9091"
	backendServers[PrimaryTag] = "localhost:9092"
	backendServers["B3"] = "localhost:9093"
	setupForGet()
	defer teardown()
	for i := 1; i <= NumRequests; i++ {
		res_chan = make(chan string, len(backendServers))
		broadcast_res, status_code := httpGet("http://localhost:9090")
		assertStatusCode(t, status_code, http.StatusOK)
		assertForPrimaryResponse(t, broadcast_res)
		waitForSecondaryResponses(res_chan)
	}
	assertMetric(t, NumRequests, "primary.success.count")
	assertMetric(t, NumRequests, "broadcaster.request.count")
}

func BenchmarkHTTPGetBroadcast(b *testing.B) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:9096"
	backendServers[PrimaryTag] = "localhost:9097"
	backendServers["B3"] = "localhost:9098"
	setupForGet()
	defer teardown()
	b.ResetTimer()
	for i := 1; i <= b.N; i++ {
		res_chan = make(chan string, len(backendServers))
		broadcast_res, status_code := httpGet("http://localhost:9090")
		assertStatusCode(b, status_code, http.StatusOK)
		assertForPrimaryResponse(b, broadcast_res)
		waitForSecondaryResponses(res_chan)
	}
	assertMetric(b, b.N, "primary.success.count")
	assertMetric(b, b.N, "broadcaster.request.count")
}

func assertStatusCode(tb testing.TB, expected_status_code, actual_status_code int) {
	if actual_status_code != expected_status_code {
		tb.Errorf("Expected status code: %d. Actual status code: %d", expected_status_code, actual_status_code)
	}
}

func assertForPrimaryResponse(tb testing.TB, response_str string) {
	if primary_tag := readTag(response_str); primary_tag != PrimaryTag {
		tb.Errorf("Expected primary tag %s, Actual primary tag %s. Broadcast Response %s", PrimaryTag, primary_tag, response_str)
	}
}

func waitForSecondaryResponses(res_chan <-chan string) {
	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()
	for i := 1; i <= len(backendServers); {
		select {
		case <-timer.C:
			timer.Reset(time.Duration(i) * time.Second)
		case <-res_chan:
			i++
			// log.Printf("Response from backend server. Tag: %s. Response: %s\n", readTag(msg), msg)
		}
	}
}
