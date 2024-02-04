package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"

	"github.com/TheLab-ms/profile/internal/reporting"
)

func (s *Server) newDocusealRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		by, _ := json.Marshal(map[string]any{"template_id": 1, "emails": user.Email})
		req, err := http.NewRequest("POST", s.Env.DocusealURL+"/api/submissions", bytes.NewBuffer(by))
		if err != nil {
			renderSystemError(w, "error while creating docuseal submission request: %s", err)
			return
		}
		req.Header.Add("X-Auth-Token", s.Env.DocusealToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			renderSystemError(w, "error sending docuseal submission request: %s", err)
			return
		}
		defer resp.Body.Close()

		subs := []struct {
			Slug string `json:"slug"`
		}{}
		err = json.NewDecoder(resp.Body).Decode(&subs)
		if err != nil {
			renderSystemError(w, "error while decoding docuseal submission: %s", err)
			return
		}
		if len(subs) == 0 {
			renderSystemError(w, "no submissions were returned from docuseal: %s", err)
			return
		}

		log.Printf("initiated docuseal submission %q for user %s", subs[0].Slug, user.Email)
		reporting.DefaultSink.Publish(user.Email, "DocusealSubmissionCreated", "created docuseal submission: %s", subs[0].Slug)
		http.Redirect(w, r, s.Env.DocusealURL+"/s/"+subs[0].Slug, http.StatusTemporaryRedirect)
	}
}

func (s *Server) newDocusealWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			Data struct {
				Email string `json:"email"`
			} `json:"data"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			log.Printf("invalid json sent to docuseal webhook endpoint: %s", err)
			w.WriteHeader(400)
			return
		}
		log.Printf("got docuseal webhook for user %s", body.Data.Email)

		user, err := s.Keycloak.GetUserByEmail(r.Context(), body.Data.Email)
		if err != nil {
			log.Printf("unable to get user by email address: %s", err)
			w.WriteHeader(500)
			return
		}

		err = s.Keycloak.UpdateUserWaiverState(r.Context(), user)
		if err != nil {
			log.Printf("error while updating user's waiver state: %s", err)
			w.WriteHeader(500)
			return
		}

		reporting.DefaultSink.Publish(body.Data.Email, "SignedWaiver", "user signed waiver")
	}
}
