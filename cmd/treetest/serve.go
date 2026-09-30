package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jan--f/docs-tree-test/internal/server"
)

func serve(ctx context.Context, app *server.Server, listen, metricsListen string) error {
	type endpoint struct {
		name    string
		address string
		handler http.Handler
	}
	endpoints := []endpoint{{"application", listen, app}}
	if metricsListen != "" {
		endpoints = append(endpoints, endpoint{"metrics", metricsListen, app.MetricsHandler()})
	}
	servers := make([]*http.Server, 0, len(endpoints))
	listeners := make([]net.Listener, 0, len(endpoints))
	// Bind everything before serving, so a metrics bind failure fails startup
	// and releases the application socket as well.
	for _, endpoint := range endpoints {
		listener, err := net.Listen("tcp", endpoint.address)
		if err != nil {
			return fmt.Errorf("%s listen: %w", endpoint.name, err)
		}
		defer listener.Close()
		listeners = append(listeners, listener)
		servers = append(servers, &http.Server{
			Handler: endpoint.handler, ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
			IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
		})
	}
	serveErrors := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { serveErrors <- srv.Serve(listeners[i]) }()
		log.Printf("%s listening on %s", endpoints[i].name, listeners[i].Addr())
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-serveErrors:
	}
	deadline, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var shutdown sync.WaitGroup
	for i, srv := range servers {
		shutdown.Go(func() {
			if err := srv.Shutdown(deadline); err != nil {
				log.Printf("%s shutdown: %v", endpoints[i].name, err)
				_ = srv.Close()
			}
		})
	}
	shutdown.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
