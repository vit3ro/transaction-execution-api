package httpapi

import "net/http"

const headerAllow = "Allow"

// ServeMux answers unknown paths and methods in plain text. This keeps every
// error in problem+json. An empty pattern means no route matched: ServeMux
// answers 405 when the path exists for other methods, and 404 otherwise. The
// 405 keeps the Allow header that ServeMux sets.
func withProblemFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}

		unmatched := &unmatchedResponse{header: http.Header{}}
		mux.ServeHTTP(unmatched, r)

		if unmatched.status == http.StatusMethodNotAllowed {
			w.Header().Set(headerAllow, unmatched.header.Get(headerAllow))
			writeProblem(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "the method is not allowed for this path")
			return
		}
		writeProblem(w, r, http.StatusNotFound, codeNotFound, "no route matches the request path")
	})
}

type unmatchedResponse struct {
	header http.Header
	status int
}

func (u *unmatchedResponse) Header() http.Header {
	return u.header
}

func (u *unmatchedResponse) WriteHeader(status int) {
	u.status = status
}

func (u *unmatchedResponse) Write(body []byte) (int, error) {
	return len(body), nil
}
