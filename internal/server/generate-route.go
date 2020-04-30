package server

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/golang-lru"
	"github.com/raphaelreyna/latte/internal/compile"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"text/template"
)

func (s *Server) handleGenerate() (http.HandlerFunc, error) {
	type delimiters struct {
		Left  string `json:"left"`
		Right string `json:"right"`
	}
	type request struct {
		// Template is base64 encoded .tex file
		Template string `json:"template"`
		// Details must be a json object
		Details map[string]interface{} `json:"details"`
		// Resources must be a json object whose keys are the resources file names and value is the base64 encoded string of the file
		Resources  map[string]string `json:"resources"`
		Delimiters *delimiters       `json:"delimiters, omitempty"`
	}
	type errorResponse struct {
		Error string `json:"error"`
		Data  string `json:"data,omitempty"`
	}
	type job struct {
		tmpl    *template.Template
		details map[string]interface{}
		dir     string
	}
	type templates struct {
		t *lru.Cache
		sync.Mutex
	}
	type resources struct {
		r *lru.Cache
		sync.Mutex
	}
	tmplsCache, err := lru.New(s.tCacheSize)
	if err != nil {
		return nil, err
	}
	rscsCache, err := lru.New(s.rCacheSize)
	if err != nil {
		return nil, err
	}
	tmpls := &templates{t: tmplsCache}
	rscs := &resources{r: rscsCache}
	return func(w http.ResponseWriter, r *http.Request) {
		// Create temporary directory into which we'll copy all of the required resource files
		// and eventually run pdflatex in.
		workDir, err := ioutil.TempDir(s.rootDir, "")
		if err != nil {
			s.errLog.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.infoLog.Printf("created new temp directory: %s", workDir)
		defer func() {
			go func() {
				if err = os.RemoveAll(workDir); err != nil {
					s.errLog.Println(err)
				}
			}()
		}()
		j := job{dir: workDir, details: map[string]interface{}{}}
		delims := delimiters{Left: "#!", Right: "!#"}
		// Grab any data sent as JSON
		if r.Header.Get("Content-Type") == "application/json" {
			var req request
			err := json.NewDecoder(r.Body).Decode(&req)
			switch {
			case err == io.EOF:
				s.respond(w, "request header Content-Type set to application/json; received empty body", http.StatusBadRequest)
				return
			case err != nil:
				s.errLog.Println(err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			r.Body.Close()
			if req.Delimiters != nil {
				d := req.Delimiters
				if d.Left == "" || d.Right == "" {
					s.respond(w, "only received one delimiter; need none or both", http.StatusBadRequest)
					return
				}
				delims = *req.Delimiters
			}
			if req.Template != "" {
				// Check if we've already parsed this template; if not, parse it and cache the results
				tHash := md5.Sum([]byte(req.Template))
				// We append template delimiters to account for the same file being uploaded with different delimiters.
				// This would really only happen on accident but not taking it into account leads to unexpected caching behavior.
				cid := hex.EncodeToString(tHash[:]) + delims.Left + delims.Right
				tmpls.Lock()
				ti, exists := tmpls.t.Get(cid)
				var t *template.Template
				if !exists {
					tBytes, err := base64.StdEncoding.DecodeString(req.Template)
					if err != nil {
						tmpls.Unlock()
						s.errLog.Println(err)
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					t = template.New(cid).Delims(delims.Left, delims.Right)
					t, err = t.Parse(string(tBytes))
					if err != nil {
						tmpls.Unlock()
						s.errLog.Println(err)
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}

					tmpls.t.Add(cid, t)
				} else {
					t = ti.(*template.Template)
				}
				j.tmpl = t
				tmpls.Unlock()
			}
			// Grab details if they were provided
			if len(req.Details) > 0 {
				j.details = req.Details
			}
			// Write resources files into working directory
			for name, data := range req.Resources {
				fname := filepath.Join(workDir, name)
				bytes, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					s.errLog.Println(err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				err = ioutil.WriteFile(fname, bytes, os.ModePerm)
				if err != nil {
					s.errLog.Println(err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}
		// Grab any ids sent over the URL
		q := r.URL.Query()
		// Grab template being requested in the URL
		if tmplID := q.Get("tmpl"); j.tmpl == nil && tmplID != "" {
			tmplID = tmplID + delims.Left + delims.Right
			tmpls.Lock()
			ti, exists := tmpls.t.Get(tmplID)
			var t *template.Template
			if !exists {
				// Try loading the template file from local disk, downloading it if it doesn't exist
				tmplPath := filepath.Join(s.rootDir, tmplID)
				var tmplBytes []byte
				_, err := os.Stat(tmplPath)
				if os.IsNotExist(err) {
					if s.db == nil {
						tmpls.Unlock()
						msg := fmt.Sprintf("template with id %s not found", tmplID)
						s.respond(w, msg, http.StatusBadRequest)
						return
					}
					rawData, err := s.db.Fetch(r.Context(), tmplID)
					switch err.(type) {
					case *NotFoundError:
						tmpls.Unlock()
						msg := fmt.Sprintf("template with id %s not found", tmplID)
						http.Error(w, msg, http.StatusInternalServerError)
						return
					default:
						if err != nil {
							tmpls.Unlock()
							s.errLog.Println(err)
							http.Error(w, err.Error(), http.StatusInternalServerError)
							return
						}
					}
					err = toDisk(rawData, tmplPath)
					if err != nil {
						tmpls.Unlock()
						s.errLog.Printf("error while writing to %s: %v", tmplPath, err)
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
				} else if err != nil {
					tmpls.Unlock()
					s.errLog.Println(err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if tmplBytes == nil {
					tmplBytes, err = ioutil.ReadFile(tmplPath)
					if err != nil {
						tmpls.Unlock()
						s.errLog.Println(err)
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
				}
				t = template.New(tmplID).Delims(delims.Left, delims.Right)
				t, err = t.Parse(string(tmplBytes))
				if err != nil {
					tmpls.Unlock()
					s.errLog.Println(err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				tmpls.t.Add(tmplID, t)
			} else {
				t = ti.(*template.Template)
			}
			j.tmpl = t
			tmpls.Unlock()
		} else if j.tmpl == nil {
			err = errors.New("no template provided")
			s.errLog.Println(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Symlink resources into the working directory, downloading those that aren't in the root directory
		rscsIDs := q["rsc"]
		for _, rscID := range rscsIDs {
			// Prevent other routines from downloading this resource if its not found and we're already downloading it.
			rscs.Lock()
			rscPathi, exists := rscs.r.Get(rscID)
			var rscPath string
			if _, err = os.Stat(rscPath); os.IsNotExist(err) || !exists {
				if s.db == nil {
					rscs.Unlock()
					msg := fmt.Sprintf("resource with id %s not found", rscID)
					s.respond(w, msg, http.StatusBadRequest)
					return
				}
				// If path not in memory, then file doesn't exit on local disk (but lets double check) and we need to download it.
				rscData, err := s.db.Fetch(r.Context(), rscID)
				switch err.(type) {
				case *NotFoundError:
					rscs.Unlock()
					msg := fmt.Sprintf("resource with id %s not found", rscID)
					http.Error(w, msg, http.StatusInternalServerError)
					return
				default:
					if err != nil {
						rscs.Unlock()
						s.errLog.Println(err)
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
				}
				rscPath = filepath.Join(s.rootDir, rscID)
				err = toDisk(rscData, rscPath)
				if err != nil {
					tmpls.Unlock()
					s.errLog.Printf("error while writing to %s: %v", rscPath, err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				rscs.r.Add(rscID, rscPath)
			} else {
				rscPath = rscPathi.(string)
			}
			rscs.Unlock()
			err = os.Symlink(rscPath, filepath.Join(workDir, rscID))
			if err != nil {
				s.errLog.Println(err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// Load and parse details json from local disk, downloading it from the db if not found on local disk
		if dtID := q.Get("dtls"); len(j.details) == 0 && dtID != "" {
			dtlsPath := filepath.Join(s.rootDir, dtID)
			_, err = os.Stat(dtlsPath)
			if os.IsNotExist(err) {
				if s.db == nil {
					msg := fmt.Sprintf("details json with id %s not found", dtID)
					er := errorResponse{Error: msg}
					w.Header().Set("Content-Type", "application/json")
					payload := s.respond(w, &er, http.StatusInternalServerError)
					s.errLog.Println("%s", payload)
					return
				}
				dtlsData, err := s.db.Fetch(r.Context(), dtID)
				switch err.(type) {
				case *NotFoundError:
					msg := fmt.Sprintf("details json with id %s not found", dtID)
					er := errorResponse{Error: msg}
					w.Header().Set("Content-Type", "application/json")
					payload := s.respond(w, &er, http.StatusInternalServerError)
					s.errLog.Println("%s", payload)
					return
				default:
					if err != nil {
						er := errorResponse{
							Error: "error while getting json file info",
							Data:  err.Error(),
						}
						w.Header().Set("Content-Type", "application/json")
						payload := s.respond(w, &er, http.StatusInternalServerError)
						s.errLog.Println("%s", payload)
						return
					}
				}
				err = toDisk(dtlsData, dtlsPath)
				if err != nil {
					er := errorResponse{
						Error: "error while writing json file to disk",
						Data:  err.Error(),
					}
					w.Header().Set("Content-Type", "application/json")
					payload := s.respond(w, &er, http.StatusInternalServerError)
					s.errLog.Println("%s", payload)
					return
				}
				switch dtlsData.(type) {
				case []byte:
					err = json.Unmarshal(dtlsData.([]byte), &j.details)
					if err != nil {
						er := errorResponse{
							Error: "error while decoding json",
							Data:  err.Error(),
						}
						w.Header().Set("Content-Type", "application/json")
						payload := s.respond(w, &er, http.StatusInternalServerError)
						s.errLog.Println("%s", payload)
						return
					}
				case io.ReadCloser:
					rc := dtlsData.(io.ReadCloser)
					err = json.NewDecoder(rc).Decode(&j.details)
					if err != nil {
						er := errorResponse{
							Error: "error while decoding json",
							Data:  err.Error(),
						}
						w.Header().Set("Content-Type", "application/json")
						payload := s.respond(w, &er, http.StatusInternalServerError)
						s.errLog.Println("%s", payload)
						return
					}
					rc.Close()
				}
			} else if err != nil {
				er := errorResponse{
					Error: "error while getting json file info",
					Data:  err.Error(),
				}
				w.Header().Set("Content-Type", "application/json")
				payload := s.respond(w, &er, http.StatusInternalServerError)
				s.errLog.Println("%s", payload)
				return
			}
			if len(j.details) == 0 {
				f, err := os.Open(dtlsPath)
				if err != nil {
					er := errorResponse{
						Error: "error while opening json file",
						Data:  err.Error(),
					}
					w.Header().Set("Content-Type", "application/json")
					payload := s.respond(w, &er, http.StatusInternalServerError)
					s.errLog.Println("%s", payload)
					return
				}
				err = json.NewDecoder(f).Decode(&j.details)
				if err != nil {
					er := errorResponse{
						Error: "error while decoding json",
						Data:  err.Error(),
					}
					w.Header().Set("Content-Type", "application/json")
					payload := s.respond(w, &er, http.StatusInternalServerError)
					s.errLog.Println("%s", payload)
					return
				}
				f.Close()
			}
		}
		// Compile pdf
		pdfPath, err := compile.Compile(r.Context(), j.tmpl, j.details, j.dir, s.cmd)
		if err != nil {
			er := &errorResponse{Error: err.Error(), Data: string(pdfPath)}
			w.Header().Set("Content-Type", "application/json")
			payload := s.respond(w, er, http.StatusInternalServerError)
			s.errLog.Printf("%s", payload)
			return
		}
		pdf, err := os.Open(filepath.Join(workDir, pdfPath))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			payload := s.respond(w, &errorResponse{Error: "encountered an error"}, http.StatusInternalServerError)
			s.errLog.Printf("%s", payload)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		io.Copy(w, pdf)
		pdf.Close()
	}, nil
}
