package profile

import (
	"embed"
	"html/template"
	"log"
)

//go:embed assets/*
var Assets embed.FS

//go:embed templates/*.html
var raw embed.FS

var Templates *template.Template

func init() {
	// Parse the embedded templates once during initialization
	var err error
	Templates, err = template.ParseFS(raw, "templates/*")
	if err != nil {
		log.Fatal(err)
	}
}
