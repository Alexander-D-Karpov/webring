package middleware

import "net/http"

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

		allowedHeaders := "Accept, Content-Type, Content-Length, Accept-Encoding, " +
			"X-CSRF-Token, Authorization"
		w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
