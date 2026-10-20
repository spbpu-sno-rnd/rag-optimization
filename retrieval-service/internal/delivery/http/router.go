package http

import (
	"net/http"
)

func NewRouter(handler *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Health)
	mux.HandleFunc("POST /v1/search", handler.Search)
	return mux
}
