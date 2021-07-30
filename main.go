package main

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/caarlos0/env/v6"

	chtml "github.com/alecthomas/chroma/formatters/html"
	"github.com/go-chi/chi"
	mathjax "github.com/litao91/goldmark-mathjax"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ghtml "github.com/yuin/goldmark/renderer/html"
)

// YASG represents all logic for the Site Generator.
type YASG struct {
	parser   goldmark.Markdown
	content  fs.FS
	staticFS fs.FS
	templ    *template.Template
	debug    bool
	once     sync.Once
}

type TemplParams struct {
	Content  template.HTML
	Metadata map[string]interface{}
}

func (s *YASG) loadTemplates() (err error) {
	s.templ, err = template.ParseFS(s.content, "layout.templ")
	return
}

func copyFS(outPath string, dir fs.FS) error {
	panic("Not implemented")
}

func (s *YASG) Generate(outPath string) error {
	if err := os.MkdirAll(outPath, 0777); err != nil {
		return err
	}

	// Static files
	/*
		staticPath := path.Join(outPath, "static")
		if err := os.MkdirAll(staticPath, 0777); err != nil {
			return err
		}
		if err := copyFS(staticPath, s.staticFS); err != nil {
			return err
		}
	*/

	// Content
	fs.WalkDir(s.content, ".", func(p string, d fs.DirEntry, err error) error {
		log.Println(p, err)
		if err != nil {
			log.Println("Error #1234:", err)
		}
		filePath := path.Join(outPath, p)
		if d.IsDir() {
			if err := os.MkdirAll(filePath, 0777); err != nil {
				log.Println("aaa", err)
				return fs.SkipDir
			}
		}
		log.Println(d.Name())
		file, err := s.content.Open(p)
		if err != nil {
			return fs.SkipDir
		}

		out, name, err := s.getFileOutput(path.Base(p), file)
		if err != nil {
			log.Println(err)
			return nil
		}

		dir, _ := path.Split(filePath)
		filePath = path.Join(dir, name)
		log.Println("plm:", filePath)

		f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			log.Println(err)
			return nil
		}
		_, err = io.Copy(f, out)
		err1 := f.Close()
		if err == nil && err1 != nil {
			err = err1
		}
		if err != nil {
			log.Println(err, err1)
			return nil
		}

		return nil
	})

	return nil
}

func must(f func() error) func() {
	return func() {
		if err := f(); err != nil {
			panic(err)
		}
	}
}

func (s *YASG) getFileOutput(filename string, r io.Reader) (io.Reader, string, error) {
	// Special cases
	s.once.Do(must(s.loadTemplates))

	switch path.Ext(filename) {
	case ".md":
		filename = strings.ReplaceAll(filename, ".md", ".html")
		md, err := io.ReadAll(r)
		if err != nil {
			return nil, filename, err
		}
		ctx := parser.NewContext()
		var buf, out bytes.Buffer
		if err := s.parser.Convert(md, &buf, parser.WithContext(ctx)); err != nil {
			log.Println(err)
			return nil, filename, err
		}

		t := TemplParams{
			Content:  template.HTML(buf.String()),
			Metadata: meta.Get(ctx),
		}

		if err := s.templ.Execute(&out, t); err != nil {
			fmt.Println(err)
			return nil, filename, err
		}
		return &out, filename, nil
	case ".body":
		filename = strings.ReplaceAll(filename, ".body", ".html")
		var out bytes.Buffer
		file, err := io.ReadAll(r)
		if err != nil {
			return nil, filename, err
		}
		t := TemplParams{
			Content:  template.HTML(file),
			Metadata: nil,
		}
		if err := s.templ.Execute(&out, t); err != nil {
			return nil, filename, err
		}
		return &out, filename, nil
	default:
		return r, filename, nil
	}
}

func (s *YASG) GetRouter() http.Handler {
	r := chi.NewRouter()

	if err := s.loadTemplates(); err != nil {
		fmt.Println(err)
		return nil
	}

	if s.debug {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := s.loadTemplates(); err != nil {
					http.Error(w, "An unexpected error occured while rendering the web page", 500)
					fmt.Println(err)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
	}

	r.Mount("/static", http.FileServer(http.FS(s.staticFS)))
	r.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasSuffix(path, "/") {
			path += "index"
		}

		path = strings.Trim(path, "/")

		if strings.HasSuffix(path, ".md") { // the request wants the raw md file
			file, err := s.content.Open(path)
			if err != nil {
				fmt.Fprintln(w, "Not Found")
				return
			}
			defer file.Close()

			st, err := file.Stat()
			if err != nil {
				fmt.Fprintln(w, "Not Found")
				return
			}

			http.ServeContent(w, r, st.Name(), st.ModTime(), file.(io.ReadSeeker))
			io.Copy(w, file)
			return
		}

		// check if an .md file exists, and if so, render it
		npath := path + ".md"
		md, err := fs.ReadFile(s.content, npath)
		if err == nil {
			ctx := parser.NewContext()
			var buf bytes.Buffer
			if err := s.parser.Convert(md, &buf, parser.WithContext(ctx)); err != nil {
				fmt.Fprintln(w, "Error")
				return
			}

			t := TemplParams{
				Content:  template.HTML(buf.String()),
				Metadata: meta.Get(ctx),
			}

			if err := s.templ.Execute(w, t); err != nil {
				fmt.Println(err)
			}
			return
		}

		// try and serve a file that has just the content (for pbshow and pbclass)
		npath = path + ".body"
		chtm, err := fs.ReadFile(s.content, npath)
		if err == nil {
			t := TemplParams{
				Content:  template.HTML(chtm),
				Metadata: nil,
			}
			if err := s.templ.Execute(w, t); err != nil {
				fmt.Println(err)
			}
			return
		}

		// try and serve a regular file
		file, err := s.content.Open(path)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			fmt.Fprintln(w, "Not Found")
			return
		} else if err != nil {
			fmt.Println(err)
			return
		}
		defer file.Close()
		st, err := file.Stat()
		if err != nil {
			fmt.Println(err)
			return
		}

		http.ServeContent(w, r, st.Name(), st.ModTime(), file.(io.ReadSeeker))
	}))

	return r
}

func New(debug bool, ffs fs.FS) (*YASG, error) {

	md := goldmark.New(
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			ghtml.WithHardWraps(),
		),
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
			meta.Meta,
			mathjax.MathJax,
			highlighting.NewHighlighting(
				highlighting.WithStyle("monokai"),
				highlighting.WithFormatOptions(
					chtml.WithLineNumbers(true),
				),
			),
		),
	)

	staticFS, err := fs.Sub(ffs, "static")
	if err != nil {
		return nil, err
	}

	contentFS, err := fs.Sub(ffs, "content")
	if err != nil {
		return nil, err
	}

	return &YASG{parser: md, content: contentFS, debug: debug, staticFS: staticFS}, nil
}

type config struct {
	Port  int    `env:"VRO_PORT" envDefault:"7000"`
	Debug bool   `env:"VRO_DEBUG" envDefault:"true"`
	Path  string `env:"VRO_PATH" envDefault:"./contents"`
}

func main() {
	cfg := config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatal(err)
	}

	staticMD, err := New(cfg.Debug, os.DirFS(cfg.Path))
	if err != nil {
		log.Fatal(err)
	}
	staticMD.Generate("./out")

	/*
		log.Printf("Listening on port %d\n", cfg.Port)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), staticMD.GetRouter()); err != nil {
			log.Fatal(err)
		}
	*/
}
