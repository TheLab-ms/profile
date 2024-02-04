package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func (s *Server) newFileUploadHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: Render a nice page instead of just this tiny form
		if r.Method == "GET" {
			w.Header().Add("Content-Type", "text/html")
			id := r.URL.Query().Get("i")
			if id == "" {
				io.WriteString(w, `
				<form method="post" enctype="multipart/form-data">
					<input type="file" name="upload"/>
					<button>Upload</button>
				</form>
				`)
			} else {
				io.WriteString(w, fmt.Sprintf("download link: %s/f/%s", s.Env.SelfURL, id))
			}
			return
		}

		claims := jwt.RegisteredClaims{}
		claims.Audience = jwt.ClaimStrings{"profile"}
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.Env.FileTokenSigningKey))
		if err != nil {
			renderSystemError(w, "error while signing token: %s", err)
			return
		}

		url := fmt.Sprintf("%s/?t=%s&r=%s/files", s.Env.FileServerURL, token, s.Env.SelfURL)
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	}
}

func (s *Server) newFileDownloadHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := jwt.RegisteredClaims{}
		claims.Audience = jwt.ClaimStrings{"profile"}
		claims.Subject = strings.TrimPrefix(r.URL.Path, "/f/")
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.Env.FileTokenSigningKey))
		if err != nil {
			renderSystemError(w, "error while signing token: %s", err)
			return
		}

		url := fmt.Sprintf("%s/?t=%s", s.Env.FileServerURL, token)
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	}
}
