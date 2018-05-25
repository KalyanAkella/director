package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
)

/*
Inspired by http://127.0.0.1:6060/src/net/http/httputil/reverseproxy.go?s=2588:2649#L80

A simple HTTP broadcaster.
*/

func cloneHeader(h http.Header) http.Header {
	h2 := make(http.Header, len(h))
	for k, vv := range h {
		vv2 := make([]string, len(vv))
		copy(vv2, vv)
		h2[k] = vv2
	}
	return h2
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
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

func newRequest(req *http.Request, req_url *url.URL) *http.Request {
	ctx := req.Context()
	new_req := req.WithContext(ctx)

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

func roundTrip(req *http.Request, transport http.RoundTripper, res_chan chan<- *http.Response, err_chan chan<- error) {
	if res, err := transport.RoundTrip(req); err == nil {
		res_chan <- res
	} else {
		err_chan <- err
	}
}

var ENDPOINTS = []string{"localhost:9091", "localhost:9092"}

func main() {
	log.Fatal(http.ListenAndServe(":9090", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		res_chan := make(chan *http.Response, len(ENDPOINTS))
		err_chan := make(chan error, len(ENDPOINTS))

		transport := http.DefaultTransport
		for _, endpoint := range ENDPOINTS {
			request := newRequest(req, &url.URL{Scheme: "http", Host: endpoint})
			go roundTrip(request, transport, res_chan, err_chan)
		}

		var responses, errors []string
		for i := 1; i <= len(ENDPOINTS); i++ {
			select {
			case res := <-res_chan:
				defer res.Body.Close()
				r, e := ioutil.ReadAll(res.Body)
				if e == nil {
					responses = append(responses, strings.TrimSpace(string(r)))
				} else {
					fmt.Printf("[ERR] %s\n", e)
				}
			case err := <-err_chan:
				errors = append(errors, strings.TrimSpace(err.Error()))
			}
		}
		response := fmt.Sprintf("Responses: %s, Errors: %s", strings.Join(responses, "|"), strings.Join(errors, "|"))
		fmt.Fprintln(rw, response)
	})))
}
