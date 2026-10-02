package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
)

// CSRFHeader carries the session's CSRF token on state-changing requests.
const CSRFHeader = "X-CSRF-Token"

// SafeMethod reports whether a method cannot change state.
func SafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// SameOrigin reports whether a request was made by a page of origin: Sec-Fetch-Site must not be cross-site or
// same-site, and Origin (or, without it, Referer) must match. A request with neither header is refused, because
// browsers always send Origin on the state-changing requests this guards.
func SameOrigin(r *http.Request, origin string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		return o == origin
	}
	ref, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || ref.Host == "" {
		return false
	}
	return ref.Scheme+"://"+ref.Host == origin
}

// ValidCSRF compares the request's CSRF header with the session's token in constant time.
func ValidCSRF(r *http.Request, sessionToken string) bool {
	got := r.Header.Get(CSRFHeader)
	return got != "" && sessionToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(sessionToken)) == 1
}
