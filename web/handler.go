package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var files embed.FS

func Handler() (http.Handler, error) {
	fsys, err := fs.Sub(files, "static")
	if err != nil {
		return nil, err
	}
	return http.FileServer(http.FS(fsys)), nil
}
