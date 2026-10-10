package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// WriteJSONError answers an /api request with {"error": msg} and the
// given status. (http.Error would reset Content-Type to text/plain.)
func WriteJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// StaticHandler serves the embedded reference web client at /.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
