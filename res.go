package main

import (
	"embed"
	"os"
	"text/template"
)

//go:embed res
var templatesData embed.FS
var tpl *template.Template

func init() {
	var err error
	tpl, err = template.ParseFS(templatesData, "res/*.template")
	if err != nil {
		panic(err)
	}
}

func getTemplate() *template.Template {
	if *developMode {
		tpl, err := template.ParseFS(os.DirFS("res"), "*.template")
		if err != nil {
			panic(err)
		}
		return tpl
	}
	return tpl
}
