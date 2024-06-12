package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func (s *Server) newSecretIndexHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ciphertext := r.URL.Query().Get("c")
		if ciphertext == "" {
			w.Header().Add("Content-Type", "text/html")
			s.Templates.ExecuteTemplate(w, "secret-index.html", nil)
			return
		}
		// The caller provided ciphertext, decrypt it

		userID := r.Header.Get("X-Forwarded-Email")
		isLeadership := strings.Contains(r.Header.Get("X-Forwarded-Groups"), "leadership")

		js := &bytes.Buffer{}
		cmd := exec.CommandContext(r.Context(), "age", "--decrypt", "-i", "key.txt")
		cmd.Stderr = os.Stderr
		cmd.Stdout = js
		cmd.Stdin = base64.NewDecoder(base64.RawURLEncoding, bytes.NewBufferString(r.URL.Query().Get("c")))
		if err := cmd.Run(); err != nil {
			log.Printf("age --decrypt failed (stderr was passed through) err=%s", err)
			http.Error(w, "decryption error or invalid input", 400)
			return
		}

		p := &secretPayload{}
		if err := json.Unmarshal(js.Bytes(), p); err != nil {
			http.Error(w, "invalid input", 400)
			return
		}

		if (p.Recipient == nil && !isLeadership) || (p.Recipient != nil && *p.Recipient != userID) {
			p.Value = "" // just in case the template somehow leaks the value
			http.Error(w, "unauthorized!", http.StatusForbidden)
			return
		}

		log.Printf("decrypted value %q for user %q originally encrypted by %q", p.Description, userID, p.EncryptedByUser)
		w.Header().Add("Content-Type", "text/html")
		s.Templates.ExecuteTemplate(w, "secret-decrypted.html", p)
	}
}

func (s *Server) newSecretEncryptionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-Forwarded-Email")

		p := &secretPayload{
			EncryptedByUser: userID,
			EncryptedAt:     time.Now().UTC().Unix(),
			Description:     r.FormValue("desc"),
			Value:           r.FormValue("value"),
		}
		if recip := r.FormValue("recip"); recip != "" {
			p.Recipient = &recip
		}
		js, err := json.Marshal(p)
		if err != nil {
			panic(err) // unlikely
		}

		ciphertext := &bytes.Buffer{}
		cmd := exec.CommandContext(r.Context(), "age", "--encrypt", "-r", s.Env.AgePublicKey)
		cmd.Stderr = os.Stderr
		cmd.Stdout = ciphertext
		cmd.Stdin = bytes.NewBuffer(js)
		if err := cmd.Run(); err != nil {
			log.Printf("age --encrypt failed (stderr was passed through) err=%s", err)
			http.Error(w, "encryption error", 500)
			return
		}

		w.Header().Add("Content-Type", "text/html")
		s.Templates.ExecuteTemplate(w, "secret-encrypted.html", map[string]any{
			"url":  s.Env.SelfURL + "/secrets?c=" + base64.RawURLEncoding.EncodeToString(ciphertext.Bytes()),
			"desc": p.Description,
		})
	}
}

type secretPayload struct {
	EncryptedByUser string  `json:"eb"`
	EncryptedAt     int64   `json:"ea"` // seconds since unix epoch utc
	Description     string  `json:"d"`
	Recipient       *string `json:"r"`
	Value           string  `json:"v"`
}
