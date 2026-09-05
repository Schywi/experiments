package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/intent"
)

type replicationIntent struct {
	WormID   string `json:"worm_id"`
	IntentID string `json:"intent_id"`
}
type response struct {
	Accepted        bool   `json:"accepted"`
	Duplicate       bool   `json:"duplicate"`
	DesiredReplicas int32  `json:"desired_replicas"`
	Error           string `json:"error,omitempty"`
}

func Handler(service intent.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /v1/replication-intents", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		var request replicationIntent
		if err := decoder.Decode(&request); err != nil {
			write(w, http.StatusBadRequest, response{Error: "invalid JSON request"})
			return
		}
		desired, duplicate, err := service.Accept(r.Context(), request.WormID, request.IntentID)
		switch {
		case err == nil:
			write(w, http.StatusOK, response{Accepted: true, Duplicate: duplicate, DesiredReplicas: desired})
		case errors.Is(err, intent.ErrCapReached):
			write(w, http.StatusConflict, response{Error: err.Error(), DesiredReplicas: desired})
		default:
			write(w, http.StatusBadRequest, response{Error: err.Error()})
		}
	})
	return mux
}
func write(w http.ResponseWriter, status int, value response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
