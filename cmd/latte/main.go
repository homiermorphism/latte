package main

import (
	"github.com/raphaelreyna/latte/internal/server"
	"log"
	"net/http"
	"os"
	"os/exec"
)

var db server.DB

func main() {
	var err error
	errLog := log.New(os.Stderr, "ERROR: ", log.Lshortfile|log.LstdFlags)
	infoLog := log.New(os.Stdout, "INFO: ", log.Lshortfile|log.LstdFlags)

	// Check for pdfLaTeX (pdfTex will do in a pinch)
	cmd := "pdflatex"
	if _, err := exec.LookPath(cmd); err != nil {
		errLog.Printf("error while searching checking pdflatex binary: %v\n\tchecking for pdftex binary", err)
		if _, err := exec.LookPath("pdftex"); err != nil {
			errLog.Fatal("neither pdflatex nor pdftex binary found in your $PATH")
		}
		infoLog.Printf("found pdftex binary; falling back to using pdftex instead of pdflatex")
		cmd = "pdftex"
	}

	// If user provides a directory path or a tex file, then run as cli tool and not as http server
	if len(os.Args) > 1 {
		if os.Args[1] != "server" {
			cli(cmd, errLog, infoLog)
			os.Exit(0)
		}
	}
	root := os.Getenv("LATTE_ROOT")
	if root == "" {
		root, err = os.UserCacheDir()
		if err != nil {
			errLog.Fatal("error creating root cache directory: %v", err)
		}
	}
	infoLog.Printf("root cache directory: %s", root)
	s, err := server.NewServer(root, cmd, db, errLog, infoLog)
	if err != nil {
		errLog.Fatal(err)
	}

	port := os.Getenv("LATTE_PORT")
	if port == "" {
		port = "27182"
	}
	infoLog.Printf("listening for HTTP traffic on port: %s ...", port)
	errLog.Fatal(http.ListenAndServe(":"+port, s))
}
