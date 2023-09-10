package main

import (
	"embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"text/template"

	"github.com/kelseyhightower/envconfig"

	"github.com/TheLab-ms/profile/conf"
	"github.com/TheLab-ms/profile/keycloak"
)

//go:embed assets/*
var assets embed.FS

//go:embed templates/*.html
var rawTemplates embed.FS

var templates *template.Template

func init() {
	// Parse the embedded templates once during initialization
	var err error
	templates, err = template.ParseFS(rawTemplates, "templates/*")
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	env := &conf.Env{}
	if err := envconfig.Process("", env); err != nil {
		log.Fatal(err)
	}

	kc := keycloak.New(env)

	// Redirect from / to /profile
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
	})

	// Signup view and registration POST handler
	http.HandleFunc("/signup", newSignupViewHandler(kc))
	http.HandleFunc("/signup/register", newRegistrationFormHandler(kc))

	// Profile view and associated form POST handlers
	http.HandleFunc("/profile", newProfileViewHandler(kc))
	http.HandleFunc("/profile/keyfob", newKeyfobFormHandler(kc))
	http.HandleFunc("/profile/contact", newContactInfoFormHandler(kc))

	// Webhooks
	http.HandleFunc("/webhooks/docuseal", newDocusealWebhookHandler(kc))

	// Embed (into the compiled binary) and serve any files from the assets directory
	http.Handle("/assets/", http.FileServer(http.FS(assets)))

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func newSignupViewHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		viewData := map[string]any{
			"page": "signup",
		}

		templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func newRegistrationFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := kc.RegisterUser(r.Context(), r.FormValue("email"))
		if err != nil {
			renderSystemError(w, "error while registering user: %s", err)
			return
		}

		viewData := map[string]any{
			"page": "signup",
		}

		templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func newProfileViewHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		viewData := map[string]any{
			"page": "profile",
			"user": user,
		}

		templates.ExecuteTemplate(w, "profile.html", viewData)
	}
}

func newKeyfobFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fobID, _ := strconv.Atoi(r.FormValue("fobid"))
		err := kc.UpdateUserFobID(r.Context(), getUserID(r), fobID)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func newContactInfoFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		first := r.FormValue("first")
		last := r.FormValue("last")

		err := kc.UpdateUserName(r.Context(), getUserID(r), first, last)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func newDocusealWebhookHandler(kc *keycloak.Keycloak) http.HandlerFunc {
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
		err := kc.UpdateUserWaiverState(r.Context(), body.Data.Email)
		if err != nil {
			log.Printf("error while updating user's waiver state: %s", err)
			w.WriteHeader(500)
			return
		}
	}
}

// getUserID allows the oauth2proxy header to be overridden for testing.
func getUserID(r *http.Request) string {
	user := r.Header.Get("X-Forwarded-Preferred-Username")
	if user == "" {
		return os.Getenv("TESTUSERID")
	}
	return user
}

func renderSystemError(w http.ResponseWriter, msg string, args ...any) {
	log.Printf(msg, args...)
	http.Error(w, "system error", 500)
}
