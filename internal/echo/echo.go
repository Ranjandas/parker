// Package echo is Parker's built-in fake service (`run: fake-service`): a tiny
// HTTP server that answers with its name, node and the request, so mesh and
// catalog repros need no user application.
package echo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

func Serve(name string, port int) error {
	node := os.Getenv("PARKER_NODE")
	msg := os.Getenv("MESSAGE")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": name, "node": node, "message": msg,
			"path": r.URL.Path, "from": r.RemoteAddr, "time": time.Now().UTC(),
		})
	})
	fmt.Printf("fake-service %s listening on :%d\n", name, port)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), mux)
}
