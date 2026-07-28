package agentbridge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxRequestBytes  = 64 << 10
	taskSecretHeader = "X-Planreader-Task-Secret"
)

type HTTPHandler struct {
	broker        *Broker
	allowedOrigin string
}

func NewHTTPHandler(broker *Broker, allowedOrigin string) http.Handler {
	return &HTTPHandler{broker: broker, allowedOrigin: strings.TrimRight(allowedOrigin, "/")}
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case strings.HasPrefix(r.URL.Path, "/task/"):
		h.serveTask(w, r)
	case strings.HasPrefix(r.URL.Path, "/browser/"):
		h.serveBrowser(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) serveBrowser(w http.ResponseWriter, r *http.Request) {
	allowedOrigin := h.allowedOrigin
	if allowedOrigin == "" {
		allowedOrigin = "http://" + r.Host
	}
	origin := r.Header.Get("Origin")
	if (r.Method != http.MethodGet && origin != allowedOrigin) || (origin != "" && origin != allowedOrigin) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/browser/turns":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var turn Turn
		if !decodeJSON(w, r, &turn) {
			return
		}
		result, err := h.broker.SubmitTurn(turn)
		writeResult(w, result, err, http.StatusCreated)
	case "/browser/events":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		if err != nil && r.URL.Query().Get("after") != "" {
			http.Error(w, "invalid event cursor", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, h.broker.Replay(after))
	case "/browser/decisions":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var decision Decision
		if !decodeJSON(w, r, &decision) {
			return
		}
		result, err := h.broker.Decide(decision)
		writeResult(w, result, err, http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) serveTask(w http.ResponseWriter, r *http.Request) {
	if !h.broker.AuthorizeTask(r.Header.Get(taskSecretHeader)) {
		http.Error(w, "invalid task credential", http.StatusUnauthorized)
		return
	}
	if err := h.broker.TaskHeartbeat(h.broker.Attachment().ID); err != nil {
		writeResult(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	switch r.URL.Path {
	case "/task/turns/wait":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		turn, err := h.broker.WaitTurn(r.Context(), r.URL.Query().Get("attachment_id"))
		writeResult(w, turn, err, http.StatusOK)
	case "/task/events":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var event Event
		if !decodeJSON(w, r, &event) {
			return
		}
		result, err := h.broker.Publish(event)
		writeResult(w, result, err, http.StatusCreated)
	case "/task/decisions":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		decision, err := h.broker.WaitDecision(r.Context(), r.URL.Query().Get("action_id"))
		writeResult(w, decision, err, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if r.ContentLength > maxRequestBytes {
		http.Error(w, "request is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			http.Error(w, "request is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "request must contain one JSON value", http.StatusBadRequest)
		return false
	}
	return true
}

func writeResult(w http.ResponseWriter, value any, err error, success int) {
	if err == nil {
		writeJSON(w, success, value)
		return
	}
	status := http.StatusConflict
	switch {
	case errors.Is(err, ErrAttachment):
		status = http.StatusGone
	case errors.Is(err, ErrInvalidState), errors.Is(err, ErrSequence):
		status = http.StatusUnprocessableEntity
	}
	http.Error(w, err.Error(), status)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
