package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type contextKey string

// Context Values' key
const (
	AttemptKey contextKey = "AttemptKey"
	RetryKey   contextKey = "RetryKey"
)

// SetRetryCount is ...
func SetRetryCount(parents context.Context, count int) context.Context {
	return context.WithValue(parents, RetryKey, count)
}

// GetRetryCount is ...
func GetRetryCount(ctx context.Context) (int, error) {
	v := ctx.Value(RetryKey)
	count, ok := v.(int)
	if !ok {
		return 0, fmt.Errorf("retry count not found")
	}
	return count, nil
}

// SetAttemptCount is ...
func SetAttemptCount(parents context.Context, count int) context.Context {
	return context.WithValue(parents, AttemptKey, count)
}

// GetAttemptCount is ...
func GetAttemptCount(ctx context.Context) (int, error) {
	v := ctx.Value(AttemptKey)
	count, ok := v.(int)
	if !ok {
		return 0, fmt.Errorf("attempt count not found")
	}
	return count, nil
}

// GetAttemptsFromContext returns the attempts for request
func GetAttemptsFromContext(r *http.Request) int {
	count, err := GetAttemptCount(r.Context())
	if err != nil {
		return count
	}
	return 1
}

// GetRetryFromContext returns the retries for request
func GetRetryFromContext(r *http.Request) int {
	count, err := GetRetryCount(r.Context())
	if err != nil {
		return count
	}
	// return 0 if there's no context value associated with key(Retry)
	return 0
}

// lb load balances the incoming request
func lb(w http.ResponseWriter, r *http.Request) {
	attempts := GetAttemptsFromContext(r)
	if attempts > 3 {
		log.Printf("%s(%s) Max attempts reached, terminating\n", r.RemoteAddr, r.URL.Path)
		http.Error(w, "Service not available", http.StatusServiceUnavailable)
		return
	}

	peer := serverPool.GetNextPeer()
	if peer != nil {
		// sends incoming request to next
		peer.ReverseProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Service not available", http.StatusServiceUnavailable)
}

// isAlive checks whether a backend is Alive by establishing a TCP connection
func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		log.Println("Site unreachable, error: ", err)
		return false
	}
	_ = conn.Close()
	return true
}

// healthCheck runs a routine for check status of the backends every 2 mins
func healthCheck() {
	t := time.NewTicker(time.Minute * 2)
	for {
		select {
		case <-t.C:
			log.Println("Starting health check...")
			serverPool.HealthCheck()
			log.Println("Health check completed")
		}
	}
}

var serverPool ServerPool

func main() {
	var serverList string
	var port int
	flag.StringVar(&serverList, "backends", "", "Load balanced backends, use commas to separate")
	flag.IntVar(&port, "port", 3030, "Port to serve")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("Please provide one or more backends to load balance")
	}

	// parse servers
	servers := strings.Split(serverList, ",")
	for _, server := range servers {
		serverURL, err := url.Parse(server)
		if err != nil {
			log.Fatal(err)
		}

		// create proxy that sends incoming request to given server URL
		proxy := httputil.NewSingleHostReverseProxy(serverURL)

		proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, e error) {
			log.Printf("[%s] error: %s\n", serverURL.Host, e.Error())
			retries := GetRetryFromContext(request)
			if retries < 3 {
				select {
				// try after
				case <-time.After(10 * time.Millisecond):
					// increment Retry count.
					ctx := SetRetryCount(request.Context(), retries+1)
					proxy.ServeHTTP(writer, request.WithContext(ctx))
				}
				return
			}

			// after 3 retries, mark this backend as down
			serverPool.MarkBackendStatus(serverURL, false)

			// if the same request routing for few attempts with different backends, increase the count
			attempts := GetAttemptsFromContext(request)
			log.Printf("%s(%s) Attempting retry %d\n", request.RemoteAddr, request.URL.Path, attempts)
			ctx := SetAttemptCount(request.Context(), attempts+1)
			lb(writer, request.WithContext(ctx))
		}

		serverPool.AddBackend(&Backend{
			URL:          serverURL,
			Alive:        true,
			ReverseProxy: proxy,
		})
		log.Printf("Configured server: %s\n", serverURL)
	}

	// create http server
	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(lb),
	}

	// start health checking
	go healthCheck()

	log.Printf("Load Balancer started at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
