package handler

import (
	"net/http"
	"sync/atomic"

	"github.com/gorilla/mux"
)

func Router(isTektonReady, isJxReady *atomic.Value) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/healthz", healthz)
	r.HandleFunc("/readyz", readyz(isTektonReady, isJxReady))
	return r
}

// healthz is a liveness probe.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// readyz is a readiness probe.
func readyz(isTektonReady, isJxReady *atomic.Value) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {

		if isTektonReady == nil || !isTektonReady.Load().(bool) {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if isJxReady == nil || !isJxReady.Load().(bool) {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
