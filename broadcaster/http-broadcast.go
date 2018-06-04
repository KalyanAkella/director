package broadcaster

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type (
	EndPointId  = string
	EndPoint    = string
	EndPoints   = map[EndPointId]EndPoint
	LoggerLevel = bool
)

type BroadcastOptions struct {
	Port            int         `yaml:"Port"`
	PrimaryEndpoint string      `yaml:"PrimaryEndpoint"`
	LogFile         string      `yaml:"LogFile"`
	LogLevel        LoggerLevel `yaml:"EnableInfoLogs"`
}

type BroadcastConfig struct {
	Options           *BroadcastOptions `yaml:"Options,omitempty"`
	Backends          EndPoints         `yaml:"Backends,omitempty"`
	primaryBackend    *url.URL
	secondaryBackends map[EndPointId]*url.URL
}

const (
	ERROR LoggerLevel = false
	INFO  LoggerLevel = true
)

var (
	currentLogLevel = ERROR
	logger          = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)

	infoLog = func(msg string) {
		if currentLogLevel == INFO {
			logger.SetPrefix("INFO:")
			logger.Println(msg)
		}
	}

	errorLog = func(msg string) {
		logger.SetPrefix("ERROR:")
		logger.Println(msg)
	}

	// Hop-by-hop headers. These are removed when sent to the backend.
	// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
	hopHeaders = []string{
		"Connection",
		"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",      // canonicalized version of "TE"
		"Trailer", // not Trailers per URL above; http://www.rfc-editor.org/errata_search.php?eid=4522
		"Transfer-Encoding",
		"Upgrade",
	}
)

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

type NoOpReporter struct{}

func (r *NoOpReporter) StartTiming() *TimingContext             { return nil }
func (r *NoOpReporter) Increment(tag string)                    {}
func (r *NoOpReporter) Gauge(tag string, value interface{})     {}
func (r *NoOpReporter) Count(tag string, value interface{})     {}
func (r *NoOpReporter) Time(tag string)                         {}
func (r *NoOpReporter) EndTiming(tc *TimingContext, tag string) {}

type Broadcaster struct {
	Handler  http.HandlerFunc
	reporter MetricsReporter
	config   *BroadcastConfig
}

func broadcastError(msg string) error {
	return fmt.Errorf("[HTTP Broadcast] %s", msg)
}

func validate(config *BroadcastConfig) error {
	if config == nil {
		return broadcastError("Configuration for broadcast must be provided")
	}
	if config.Options == nil {
		return broadcastError("Broadcast options are missing")
	}
	configureLogger(config.Options)
	if config.Options.Port == 0 {
		return broadcastError("Broadcast port is missing in broadcast options")
	}
	if config.Options.PrimaryEndpoint == "" {
		return broadcastError("Primary endpoint is missing in broadcast options")
	}
	if config.Backends == nil || len(config.Backends) == 0 {
		return broadcastError("Backends are missing or empty")
	} else {
		config.secondaryBackends = make(map[EndPointId]*url.URL)
	}
	if _, present := config.Backends[config.Options.PrimaryEndpoint]; !present {
		return broadcastError("Primary backend missing from the given set of backends")
	}
	for k, v := range config.Backends {
		if v == "" {
			return broadcastError(fmt.Sprintf("Backend endpoint with ID: %s does not have any associated data", k))
		} else {
			if backend_url, err := url.Parse(v); err != nil {
				return broadcastError(fmt.Sprintf("Invalid url: %s for endpoint with ID: %s. Error: %s", v, k, err.Error()))
			} else {
				if k == config.Options.PrimaryEndpoint {
					config.primaryBackend = backend_url
				} else {
					config.secondaryBackends[k] = backend_url
				}
			}
		}
	}
	return nil
}

func cloneHeader(h http.Header) http.Header {
	h2 := make(http.Header, len(h))
	for k, vv := range h {
		vv2 := make([]string, len(vv))
		copy(vv2, vv)
		h2[k] = vv2
	}
	return h2
}

func configureLogger(options *BroadcastOptions) {
	currentLogLevel = options.LogLevel
	broadcastLogFile := options.LogFile
	if broadcastLogFile != "" {
		if logFile, err := os.OpenFile(broadcastLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644); err == nil {
			logger.SetOutput(logFile)
		} else {
			errorLog(err.Error())
		}
	}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	slashb := strings.HasPrefix(b, "/")
	switch {
	case aslash && slashb:
		return a + b[1:]
	case !aslash && !slashb:
		return a + "/" + b
	}
	return a + b
}

func modifyRequestForBroadcast(out_req *http.Request, target *url.URL) {
	targetQuery := target.RawQuery
	out_req.URL.Scheme = target.Scheme
	out_req.URL.Host = target.Host
	out_req.URL.Path = singleJoiningSlash(target.Path, out_req.URL.Path)
	if targetQuery == "" || out_req.URL.RawQuery == "" {
		out_req.URL.RawQuery = targetQuery + out_req.URL.RawQuery
	} else {
		out_req.URL.RawQuery = targetQuery + "&" + out_req.URL.RawQuery
	}
	if _, ok := out_req.Header["User-Agent"]; !ok {
		// explicitly disable User-Agent so it's not set to default value
		out_req.Header.Set("User-Agent", "")
	}
	out_req.Host = ""
}

func newRequest(req *http.Request, req_url *url.URL) *http.Request {
	new_req := req.WithContext(context.Background())

	if req.ContentLength == 0 {
		new_req.Body = nil
	}
	new_req.Header = cloneHeader(req.Header)
	modifyRequestForBroadcast(new_req, req_url)
	new_req.Close = false

	for _, h := range hopHeaders {
		v := new_req.Header.Get(h)
		if v != "" {
			if h == "Connection" {
				for _, f := range strings.Split(v, ",") {
					if f = strings.TrimSpace(f); f != "" {
						new_req.Header.Del(f)
					}
				}
			} else {
				new_req.Header.Del(h)
			}
		}
	}
	return new_req
}

func requestToBackend(req *http.Request, id EndPointId, endpoint *url.URL, reporter MetricsReporter, metricPrefix string) (*http.Response, error) {
	new_req := req.WithContext(context.Background())
	tc := reporter.StartTiming()
	defer reporter.EndTiming(tc, fmt.Sprintf("%s.response_time", metricPrefix))
	transport := http.DefaultTransport
	if res, err := transport.RoundTrip(new_req); err == nil {
		infoLog(fmt.Sprintf("Received response with status %d from [%s]:[%s]", res.StatusCode, id, endpoint))
		reporter.Increment(fmt.Sprintf("%s.success.count", metricPrefix))
		return res, nil
	} else {
		reporter.Increment(fmt.Sprintf("%s.failure.count", metricPrefix))
		errorLog(fmt.Sprintf("Error response from [%s]:[%s] -> %s", id, endpoint, err.Error()))
		return nil, err
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func copyResponse(rw http.ResponseWriter, res *http.Response) {
	copyHeader(rw.Header(), res.Header)
	rw.WriteHeader(res.StatusCode)
	defer res.Body.Close()
	buf := make([]byte, 32*1024)
	if _, err := io.CopyBuffer(rw, res.Body, buf); err != nil {
		fmt.Fprintln(rw, string(err.Error()))
	}
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
}

func logResponse(res *http.Response) {
	defer res.Body.Close()
	var buf bytes.Buffer
	writer := bufio.NewWriter(&buf)
	io.Copy(writer, res.Body)
	writer.Flush()
	infoLog(buf.String())
}

func (b *Broadcaster) handler(rw http.ResponseWriter, req *http.Request) {
	b.reporter.Increment("broadcaster.request.count")
	infoLog("Received request: " + req.URL.String())

	primary_endpoint_id := b.config.Options.PrimaryEndpoint
	primary_backend := b.config.primaryBackend
	primary_request := newRequest(req, primary_backend)
	infoLog(fmt.Sprintf("Sending request to primary endpoint [%s]: %s", primary_endpoint_id, primary_request.URL.String()))
	if res, err := requestToBackend(primary_request, primary_endpoint_id, b.config.primaryBackend, b.reporter, "primary"); err == nil {
		copyResponse(rw, res)
	} else {
		rw.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(rw, string(err.Error()))
	}

	for id, secondary_backend := range b.config.secondaryBackends {
		secondary_request := newRequest(req, secondary_backend)
		infoLog(fmt.Sprintf("Sending request to secondary endpoint [%s]: %s", id, secondary_request.URL.String()))
		go func() {
			if res, _ := requestToBackend(secondary_request, id, secondary_backend, b.reporter, "secondary"); res != nil {
				logResponse(res)
			}
		}()
	}
}

func NewBroadcaster(broadcastConfig *BroadcastConfig) (*Broadcaster, error) {
	if err := validate(broadcastConfig); err != nil {
		return nil, err
	}
	broadcaster := &Broadcaster{
		reporter: &NoOpReporter{},
		config:   broadcastConfig,
	}
	broadcaster.Handler = http.HandlerFunc(broadcaster.handler)
	return broadcaster, nil
}

func (b *Broadcaster) WithMetricsReporter(reporter MetricsReporter) {
	if reporter != nil {
		b.reporter = reporter
	}
}

func (b *Broadcaster) ListenAndServe() error {
	return http.ListenAndServe(fmt.Sprintf(":%d", b.config.Options.Port), b.Handler)
}
