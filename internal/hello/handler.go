package hello

import (
	"encoding/json"
	"net/http"
)

type response struct {
	Message string `json:"message"`
	Service string `json:"service"`
}

// Register exposes the first locally implemented example requirement. It is
// intentionally read-only and has no dependency on external credentials.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("/hello", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(writer).Encode(response{
			Message: "Hello, World!",
			Service: "ai-sdlc-factory",
		})
	})
}
