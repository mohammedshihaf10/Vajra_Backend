package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"ocpp-server/internal/ocpp"
)

func main() {
	addr := envOrDefault("OCPP_LISTEN_ADDR", ":8090")
	apiKey := strings.TrimSpace(os.Getenv("OCPP_SERVICE_API_KEY"))
	webhookURL := strings.TrimSpace(os.Getenv("BACKEND_OCPP_WEBHOOK_URL"))
	webhookSecret := os.Getenv("OCPP_WEBHOOK_SECRET")

	timeout := 10
	if v := strings.TrimSpace(os.Getenv("OCPP_HTTP_TIMEOUT_SEC")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = n
		}
	}

	server := ocpp.NewServer(webhookURL, webhookSecret, timeout)

	mux := http.NewServeMux()

	mux.HandleFunc("/ocpp", func(w http.ResponseWriter, r *http.Request) {
		server.HandleWS(w, r)
	})

	mux.HandleFunc("/ocpp/", func(w http.ResponseWriter, r *http.Request) {
		cpID := strings.TrimPrefix(r.URL.Path, "/ocpp/")
		cpID = strings.Trim(cpID, "/")
		if cpID == "" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		q.Set("id", cpID)
		r.URL.RawQuery = q.Encode()
		server.HandleWS(w, r)
	})

	mux.HandleFunc("/v1/chargers/", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAPIKey(w, r, apiKey) {
			return
		}
		server.HandleChargerReadAPI(w, r)
	})

	mux.HandleFunc("/v1/commands/remote-start", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAPIKey(w, r, apiKey) {
			return
		}
		server.HandleRemoteStart(w, r)
	})

	mux.HandleFunc("/v1/commands/remote-stop", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAPIKey(w, r, apiKey) {
			return
		}
		server.HandleRemoteStop(w, r)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	log.Printf("ocpp-server listening on %s", addr)
	if webhookURL == "" {
		log.Println("warning: BACKEND_OCPP_WEBHOOK_URL is empty, events will not be delivered")
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func envOrDefault(key string, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func authorizeAPIKey(w http.ResponseWriter, r *http.Request, apiKey string) bool {
	if apiKey == "" {
		return true
	}
	if r.Header.Get("X-API-Key") != apiKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}
