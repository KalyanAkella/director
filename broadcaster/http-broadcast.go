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
	"strconv"
	"strings"
	"time"
)

type (
	BroadcastOption  = byte
	BroadcastOptions = map[BroadcastOption]string
	EndPointId       = string
	EndPoint         = *url.URL
	EndPoints        = map[EndPointId]EndPoint
	BroadcastConfig  = struct {
		Options  BroadcastOptions
		Backends EndPoints
	}
)

const (
	PORT BroadcastOption = iota
	PRIMARY
	RESPONSE_TIMEOUT_IN_SECS
)

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
	if _, present := config.Options[PORT]; !present {
		return broadcastError("Broadcast port is missing in broadcast options")
	}
	if _, present := config.Options[PRIMARY]; !present {
		return broadcastError("Primary endpoint is missing in broadcast options")
	}
	if _, present := config.Options[RESPONSE_TIMEOUT_IN_SECS]; !present {
		return broadcastError("Response timeout is missing in broadcast options")
	}
	if _, err := strconv.Atoi(config.Options[RESPONSE_TIMEOUT_IN_SECS]); err != nil {
		return broadcastError(err.Error())
	}
	if config.Backends == nil || len(config.Backends) == 0 {
		return broadcastError("Backends are missing or empty")
	}
	if _, present := config.Backends[config.Options[PRIMARY]]; !present {
		return broadcastError("Primary backend missing from the given set of backends")
	}
	for k, v := range config.Backends {
		if v == nil {
			return broadcastError(fmt.Sprintf("Backend endpoint with ID: %s does not have any associated data", k))
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

var (
	logger  = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)
	infoLog = func(msg string) {
		logger.SetPrefix("INFO:")
		logger.Println(msg)
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

func newRequest(req *http.Request, req_url *url.URL) *http.Request {
	new_req := req.WithContext(context.Background())

	if req.ContentLength == 0 {
		new_req.Body = nil
	}
	new_req.Header = cloneHeader(req.Header)
	new_req.URL = req_url
	new_req.Close = false

	// Remove hop-by-hop headers listed in the "Connection" header.
	// See RFC 2616, section 14.10.
	if c := new_req.Header.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			if f = strings.TrimSpace(f); f != "" {
				new_req.Header.Del(f)
			}
		}
	}

	// Remove hop-by-hop headers to the backend. Especially
	// important is "Connection" because we want a persistent
	// connection, regardless of what the client sent to us.
	for _, h := range hopHeaders {
		if new_req.Header.Get(h) != "" {
			new_req.Header.Del(h)
		}
	}
	return new_req
}

func requestToPrimary(req *http.Request, id EndPointId, endpoint EndPoint, res_chan chan<- *http.Response, err_chan chan<- error) {
	transport := http.DefaultTransport
	res, err := transport.RoundTrip(req)
	if err == nil {
		res_chan <- res
	} else {
		errorLog(fmt.Sprintf("Error response from [%s]:[%s] -> %s", id, endpoint, err.Error()))
		err_chan <- err
	}
}

func requestToSecondary(req *http.Request, id EndPointId, endpoint EndPoint) {
	transport := http.DefaultTransport
	if res, err := transport.RoundTrip(req); err != nil {
		errorLog(fmt.Sprintf("Error response from [%s]:[%s] -> %s", id, endpoint, err.Error()))
	} else {
		defer res.Body.Close()
		var buf bytes.Buffer
		writer := bufio.NewWriter(&buf)
		io.Copy(writer, res.Body)
		writer.Flush()
		infoLog(buf.String())
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func readResponseTimeout(config *BroadcastConfig) time.Duration {
	n, _ := strconv.Atoi(config.Options[RESPONSE_TIMEOUT_IN_SECS])
	return time.Duration(n) * time.Second
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

func broadcastHandler(config *BroadcastConfig) http.HandlerFunc {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {

		res_chan := make(chan *http.Response)
		err_chan := make(chan error)

		primary_endpoint_id := config.Options[PRIMARY]
		for id, endpoint := range config.Backends {
			request := newRequest(req, endpoint)
			switch id {
			case primary_endpoint_id:
				go requestToPrimary(request, id, endpoint, res_chan, err_chan)
			default:
				go requestToSecondary(request, id, endpoint)
			}
		}

		response_timeout := readResponseTimeout(config)
		select {
		case res := <-res_chan:
			copyResponse(rw, res)
		case err := <-err_chan:
			fmt.Fprintln(rw, string(err.Error()))
		case <-time.After(response_timeout):
			fmt.Fprintln(rw, "Timeout") //TODO Handle this correctly
		}
	})
}

func BroadcastHTTPHandler(config *BroadcastConfig) (http.HandlerFunc, error) {
	if err := validate(config); err != nil {
		return nil, err
	}
	return broadcastHandler(config), nil
}

func ServeOnHTTP(config *BroadcastConfig) error {
	if err := validate(config); err != nil {
		return err
	}
	return http.ListenAndServe(fmt.Sprintf(":%s", config.Options[PORT]), broadcastHandler(config))
}
