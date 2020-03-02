package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
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

type ProxyOptions struct {
	Port                int         `yaml:"Port"`
	PrimaryEndpoint     string      `yaml:"PrimaryEndpoint"`
	LogFile             string      `yaml:"LogFile"`
	LogLevel            LoggerLevel `yaml:"EnableInfoLogs"`
	MaxIdleConns        int         `yaml:"MaxIdleConns"`
	MaxIdleConnsPerHost int         `yaml:"MaxIdleConnsPerHost"`
}

type ProxyConfig struct {
	Options           *ProxyOptions `yaml:"Options,omitempty"`
	Backends          EndPoints     `yaml:"Backends,omitempty"`
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

type Director struct {
	Handler  http.HandlerFunc
	reporter MetricsReporter
	config   *ProxyConfig
}

func proxyError(msg string) error {
	return fmt.Errorf("[HTTP Proxy] %s", msg)
}

func validate(config *ProxyConfig) error {
	if config == nil {
		return proxyError("Configuration for proxy must be provided")
	}
	if config.Options == nil {
		return proxyError("Proxy options are missing")
	}
	configureLogger(config.Options)
	if config.Options.Port == 0 {
		return proxyError("Proxy port is missing in proxy options")
	}
	if config.Options.PrimaryEndpoint == "" {
		return proxyError("Primary endpoint is missing in proxy options")
	}
	if config.Backends == nil || len(config.Backends) == 0 {
		return proxyError("Backends are missing or empty")
	} else {
		config.secondaryBackends = make(map[EndPointId]*url.URL)
	}
	if _, present := config.Backends[config.Options.PrimaryEndpoint]; !present {
		return proxyError("Primary backend missing from the given set of backends")
	}
	for k, v := range config.Backends {
		if v == "" {
			return proxyError(fmt.Sprintf("Backend endpoint with ID: %s does not have any associated data", k))
		} else {
			if backend_url, err := url.Parse(v); err != nil {
				return proxyError(fmt.Sprintf("Invalid url: %s for endpoint with ID: %s. Error: %s", v, k, err.Error()))
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

func configureLogger(options *ProxyOptions) {
	currentLogLevel = options.LogLevel
	proxyLogFile := options.LogFile
	if proxyLogFile != "" {
		if logFile, err := os.OpenFile(proxyLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644); err == nil {
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

func modifyRequestForProxy(out_req *http.Request, target *url.URL) {
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

func newRequest(req *http.Request, req_body []byte, req_url *url.URL) *http.Request {
	new_req := req.WithContext(context.Background())

	new_req.ContentLength = int64(len(req_body))
	new_req.Body = ioutil.NopCloser(bytes.NewReader(req_body))
	new_req.Header = cloneHeader(req.Header)
	modifyRequestForProxy(new_req, req_url)
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

func requestToBackend(req *http.Request, id EndPointId, endpoint *url.URL, reporter MetricsReporter, metricPrefix string, options *ProxyOptions) (*http.Response, error) {
	tc := reporter.StartTiming()
	defer reporter.EndTiming(tc, fmt.Sprintf("%s.response_time", metricPrefix))
	transport := http.DefaultTransport
	transport.(*http.Transport).MaxIdleConns = options.MaxIdleConns
	transport.(*http.Transport).MaxIdleConnsPerHost = options.MaxIdleConnsPerHost
	if res, err := transport.RoundTrip(req); err == nil {
		go infoLog(fmt.Sprintf("Received response with status %d from [%s]:[%s]", res.StatusCode, id, endpoint))
		go reporter.Increment(fmt.Sprintf("%s.success.count", metricPrefix))
		return res, nil
	} else {
		go reporter.Increment(fmt.Sprintf("%s.failure.count", metricPrefix))
		go errorLog(fmt.Sprintf("Error response from [%s]:[%s] -> %s", id, endpoint, err.Error()))
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

func readRequestBody(req *http.Request) []byte {
	if buff, err := ioutil.ReadAll(req.Body); err != nil {
		errorLog(fmt.Sprintf("An error occurred while reading request body. Error: %s", err.Error()))
		return nil
	} else {
		return buff
	}
}

func (b *Director) handler(rw http.ResponseWriter, req *http.Request) {
	go b.reporter.Increment("director.request.count")
	go infoLog("Received request: " + req.URL.String())

	primary_endpoint_id := b.config.Options.PrimaryEndpoint
	primary_backend := b.config.primaryBackend
	body := readRequestBody(req)
	primary_request := newRequest(req, body, primary_backend)
	go infoLog(fmt.Sprintf("Sending request to primary endpoint [%s]: %s", primary_endpoint_id, primary_request.URL.String()))
	if res, err := requestToBackend(primary_request, primary_endpoint_id, b.config.primaryBackend, b.reporter, "primary", b.config.Options); err == nil {
		copyResponse(rw, res)
	} else {
		rw.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(rw, string(err.Error()))
	}

	go func() {
		for id, secondary_backend := range b.config.secondaryBackends {
			secondary_request := newRequest(req, body, secondary_backend)
			infoLog(fmt.Sprintf("Sending request to secondary endpoint [%s]: %s", id, secondary_request.URL.String()))
			go func() {
				if res, _ := requestToBackend(secondary_request, id, secondary_backend, b.reporter, "secondary", b.config.Options); res != nil {
					logResponse(res)
				}
			}()
		}
	}()
}

func NewDirector(proxyConfig *ProxyConfig) (*Director, error) {
	if err := validate(proxyConfig); err != nil {
		return nil, err
	}
	director := &Director{
		reporter: &NoOpReporter{},
		config:   proxyConfig,
	}
	director.Handler = http.HandlerFunc(director.handler)
	return director, nil
}

func (b *Director) WithMetricsReporter(reporter MetricsReporter) {
	if reporter != nil {
		b.reporter = reporter
	}
}

func (b *Director) ListenAndServe() error {
	return http.ListenAndServe(fmt.Sprintf(":%d", b.config.Options.Port), b.Handler)
}