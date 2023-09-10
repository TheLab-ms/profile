package main

import (
	"embed"
	"errors"
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
	http.HandleFunc("/register", newRegistrationFormHandler(kc))

	// Profile view and associated form POST handlers
	http.HandleFunc("/profile", newProfileViewHandler(kc))
	http.HandleFunc("/profile/keyfob", newKeyfobFormHandler(kc))
	http.HandleFunc("/profile/contact", newContactInfoFormHandler(kc))

	// Embed (into the compiled binary) and serve any files from the assets directory
	http.Handle("/assets/", http.FileServer(http.FS(assets)))

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func newSignupViewHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		viewData := map[string]any{
			"page": "signup",
		}

		err := templates.ExecuteTemplate(w, "signup.html", viewData)
		if err != nil {
			panic(err)
		}
	}
}

func newRegistrationFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := kc.RegisterUser(r.Context(), r.FormValue("email"))
		if err != nil {
			panic(err) // TODO
		}

		viewData := map[string]any{
			"page": "signup",
		}

		// TODO: Render banner
		err = templates.ExecuteTemplate(w, "signup.html", viewData)
		if err != nil {
			panic(err)
		}
	}
}

func newProfileViewHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUser(r.Context(), getUserID(r))
		if errors.Is(err, keycloak.ErrNotFound) {
			panic("USER NOT FOUND TODO")
		}
		if err != nil {
			panic(err) // TODO
		}

		viewData := map[string]any{
			"page": "profile",
			"user": user,
		}

		err = templates.ExecuteTemplate(w, "profile.html", viewData)
		if err != nil {
			panic(err) // TODO
		}
	}
}

func newKeyfobFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fobID, _ := strconv.Atoi(r.FormValue("fobid"))
		err := kc.UpdateUserFobID(r.Context(), getUserID(r), fobID)
		if err != nil {
			panic(err) // TODO
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
			panic(err) // TODO
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// getUserID allows the oauth2proxy header to be overridden for testing.
func getUserID(r *http.Request) string {
	user := r.Header.Get("X-Forwarded-User")
	if user == "" {
		return os.Getenv("TESTUSERID")
	}
	return user
}
