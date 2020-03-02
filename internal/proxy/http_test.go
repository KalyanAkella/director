package proxy

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

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

func newDirectorServer(handler http.HandlerFunc) *httptest.Server {
	aServer := httptest.NewUnstartedServer(handler)
	aServer.Listener = newListener(fmt.Sprintf("localhost:%d", DirectorServerPort))
	aServer.Start()
	return aServer
}

func getHandler(tag string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			log.Fatal(err)
		}
		response := fmt.Sprintf("%s %v", tag, asMap(r.Form))
		res_chan <- response
		fmt.Fprint(w, response)
	})
}

func postHandler(tag string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			log.Fatal(err)
		}
		response := fmt.Sprintf("%s %v", tag, asMap(r.Form))
		res_chan <- response
		fmt.Fprint(w, response)
	})
}

func handler(tag string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			log.Fatal(err)
		}
		response := fmt.Sprintf("%s %v", tag, asMap(r.Form))
		res_chan <- response
		fmt.Fprint(w, response)
	})
}

func newServer(tag, endpoint string) *httptest.Server {
	aServer := httptest.NewUnstartedServer(handler(tag))
	aServer.Listener = newListener(endpoint)
	aServer.Start()
	return aServer
}

const (
	PrimaryTag         = "B2"
	DirectorServerPort = 9090
	NumRequests        = 10
)

func parseResponse(response string) (string, string) {
	var tag, message string
	if _, err := fmt.Sscanf(response, "%s %s", &tag, &message); err != nil {
		log.Fatal(err)
	}
	return tag, message
}

func asValues(data map[string]string) url.Values {
	values := url.Values{}
	for k, v := range data {
		values.Set(k, v)
	}
	return values
}

func asMap(values url.Values) map[string]string {
	result := make(map[string]string)
	for k, vs := range values {
		if len(vs) == 0 {
			result[k] = ""
		} else {
			result[k] = vs[0]
		}
	}
	return result
}

func httpPost(httpUrl string, data map[string]string) (string, int) {
	if res, err := http.PostForm(httpUrl, asValues(data)); err != nil {
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

func httpGet(httpUrl string, data map[string]string) (string, int) {
	if res, err := http.Get(fmt.Sprintf("%s?%s", httpUrl, asValues(data).Encode())); err != nil {
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

var proxy_server *httptest.Server
var res_chan chan string
var backends map[string]*httptest.Server
var backendServers map[string]string
var reporter *Reporter

func startBackendServers() {
	backends = make(map[string]*httptest.Server)
	for t, e := range backendServers {
		backends[t] = newServer(t, e)
	}
}

func startDirectorServer() {
	servers := make(map[string]string, len(backendServers))
	for t, e := range backendServers {
		servers[t] = fmt.Sprintf("http://%s", e)
	}
	if director, err := NewDirector(&ProxyConfig{
		Backends: servers,
		Options: &ProxyOptions{
			Port:            DirectorServerPort,
			PrimaryEndpoint: PrimaryTag,
			LogLevel:        ERROR,
		},
	}); err != nil {
		log.Fatal(err)
	} else {
		reporter = &Reporter{metrics: make(map[string]uint64)}
		director.WithMetricsReporter(reporter)
		proxy_server = newDirectorServer(director.Handler)
	}
}

func setup() {
	startBackendServers()
	startDirectorServer()
}

func teardown() {
	shutdownBackend(proxy_server)
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

func TestHTTPGetWithFailureResponse(t *testing.T) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:9094"
	backendServers[PrimaryTag] = "localhost:9095"
	setup()
	defer teardown()
	shutdownBackend(backends[PrimaryTag])
	_, status_code := httpGet("http://localhost:9090", map[string]string{})
	assertStatusCode(t, status_code, http.StatusServiceUnavailable)
	assertMetric(t, 1, "primary.failure.count")
	assertMetric(t, 1, "director.request.count")
}

func TestHTTPPostWithSuccessResponse(t *testing.T) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:8091"
	backendServers[PrimaryTag] = "localhost:8092"
	backendServers["B3"] = "localhost:8093"
	setup()
	defer teardown()
	for i := 1; i <= NumRequests; i++ {
		res_chan = make(chan string, len(backendServers))
		data := map[string]string{"index": strconv.Itoa(i)}
		director_res, status_code := httpPost("http://localhost:9090", data)
		assertStatusCode(t, status_code, http.StatusOK)
		assertForPrimaryResponse(t, director_res, data)
		waitForSecondaryResponses(res_chan)
	}
	assertMetric(t, NumRequests, "primary.success.count")
	assertMetric(t, NumRequests, "director.request.count")
}

func TestHTTPGetWithSuccessResponse(t *testing.T) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:9091"
	backendServers[PrimaryTag] = "localhost:9092"
	backendServers["B3"] = "localhost:9093"
	setup()
	defer teardown()
	for i := 1; i <= NumRequests; i++ {
		res_chan = make(chan string, len(backendServers))
		data := map[string]string{"index": strconv.Itoa(i)}
		director_res, status_code := httpGet("http://localhost:9090", data)
		assertStatusCode(t, status_code, http.StatusOK)
		assertForPrimaryResponse(t, director_res, data)
		waitForSecondaryResponses(res_chan)
	}
	assertMetric(t, NumRequests, "primary.success.count")
	assertMetric(t, NumRequests, "director.request.count")
}

func BenchmarkHTTPGet(b *testing.B) {
	backendServers = make(map[string]string)
	backendServers["B1"] = "localhost:9096"
	backendServers[PrimaryTag] = "localhost:9097"
	backendServers["B3"] = "localhost:9098"
	setup()
	defer teardown()
	b.ResetTimer()
	for i := 1; i <= b.N; i++ {
		res_chan = make(chan string, len(backendServers))
		data := map[string]string{"index": strconv.Itoa(i)}
		director_res, status_code := httpGet("http://localhost:9090", data)
		assertStatusCode(b, status_code, http.StatusOK)
		assertForPrimaryResponse(b, director_res, data)
		waitForSecondaryResponses(res_chan)
	}
	assertMetric(b, b.N, "primary.success.count")
	assertMetric(b, b.N, "director.request.count")
}

func assertStatusCode(tb testing.TB, expected_status_code, actual_status_code int) {
	if actual_status_code != expected_status_code {
		tb.Errorf("Expected status code: %d. Actual status code: %d", expected_status_code, actual_status_code)
	}
}

func assertForPrimaryResponse(tb testing.TB, response_str string, data map[string]string) {
	if primary_tag, message := parseResponse(response_str); primary_tag != PrimaryTag {
		tb.Errorf("Expected primary tag %s, Actual primary tag %s. Director Response %s", PrimaryTag, primary_tag, response_str)
	} else {
		expected_message := fmt.Sprintf("%v", data)
		if expected_message != message {
			tb.Errorf("Expected message '%s'. Actual message '%s'", expected_message, message)
		}
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
		}
	}
}
