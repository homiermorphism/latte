package compile

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

func Compile(tmpl *template.Template, dtls map[string]interface{}, dir string) (string, error) {
	os.Chdir(dir)
	// Prepare pdflatex and grab a pipe to its stdin
	jn := filepath.Base(dir)
	cmd := exec.Command("pdflatex", "-halt-on-error", "-jobname="+jn)
	cmdStdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	// Write filled in template to pdflatex stdin
	err = tmpl.Execute(cmdStdin, dtls)
	if err != nil {
		return "", err
	}
	cmdStdin.Close()

	// Run command and grab its output and log it
	result, err := cmd.Output()
	if err != nil {
		return "", err
	}
	log.Println(string(result))
	os.Chdir("..")
	return jn + ".pdf", nil
}