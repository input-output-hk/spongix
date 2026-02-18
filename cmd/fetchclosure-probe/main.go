// fetchclosure-probe is a tiny HTTP proxy that logs every request Nix makes
// when evaluating builtins.fetchClosure. It helps determine which store paths
// Nix looks up (fromPath hash vs toPath hash) and in what order.
//
// Usage:
//
//	go run ./cmd/fetchclosure-probe --upstream http://alpha:5000/cache --listen :9999
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	upstream := flag.String("upstream", "", "upstream cache URL to proxy to")
	listen := flag.String("listen", ":9999", "listen address")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("--upstream is required")
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("parsing upstream URL: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("%s %s\n", r.Method, r.URL.Path)
		for k, v := range r.Header {
			fmt.Printf("  %s: %v\n", k, v)
		}
		fmt.Println()

		r.Host = target.Host
		proxy.ServeHTTP(w, r)
	})

	log.Printf("listening on %s, proxying to %s", *listen, *upstream)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
