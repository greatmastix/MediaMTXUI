package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The request log names a WebRTC session's route, not its id: the URL alone can change or end the session.
func TestLoggedPathHidesSessionIDs(t *testing.T) {
	var got string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			got = loggedPath(req)
		})
	})
	ok := func(http.ResponseWriter, *http.Request) {}
	r.Delete("/rtc-session/{id}", ok)
	r.Route("/api", func(api chi.Router) {
		api.Patch("/v1/live/whep-session/{id}", ok)
		api.Get("/v1/streams/{id}", ok)
	})
	for _, c := range []struct{ method, path, want string }{
		{"DELETE", "/rtc-session/0b9f3c2e", "/rtc-session/{id}"},
		{"PATCH", "/api/v1/live/whep-session/7a1d", "/api/v1/live/whep-session/{id}"},
		{"GET", "/api/v1/streams/12", "/api/v1/streams/12"},
		{"GET", "/nowhere/rtc-session/x", "/nowhere/rtc-session/x"},
	} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
		if got != c.want {
			t.Errorf("%s %s logged as %s, want %s", c.method, c.path, got, c.want)
		}
	}
}
