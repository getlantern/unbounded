package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/getlantern/broflake/report"
)

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		userID := r.Header.Get(report.HeaderUserID)
		teamID := r.Header.Get(report.HeaderTeamID)
		connID := r.Header.Get(report.HeaderConnID)
		if userID == "" || teamID == "" || connID == "" {
			http.Error(w, "missing headers", http.StatusBadRequest)
			return
		}
		slog.Info("received report",
			"teamID", teamID,
			"userID", userID,
			"connID", connID,
		)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func main() {
	slog.Info("starting mock reporting server", "port", report.EndpointPort)
	http.HandleFunc("/", metricsHandler)
	err := http.ListenAndServe(fmt.Sprintf(":%v", report.EndpointPort), nil)
	if err != nil {
		slog.Error("failed to start server", "error", err)
	}
}
