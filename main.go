package main

import (
	"bytes"
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
	return fs.WalkDir(dir, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip hidden files and directories
		if len(path.Base(p)) > 1 && path.Base(p)[0] == '.' {
			return nil
		}

		filePath := path.Join(outPath, p)
		if d.IsDir() {
			if err := os.MkdirAll(filePath, 0777); err != nil {
				log.Println("Couldn't mkdir", err)
				return err
			}
			return nil
		}
		file, err := dir.Open(p)
		if err != nil {
			return err
		}

		f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, file)
		err1 := f.Close()
		if err == nil && err1 != nil {
			err = err1
		}
		if err != nil {
			return err
		}
		return nil
	})
}

func (s *YASG) Generate(outPath string) error {
	if err := os.MkdirAll(outPath, 0777); err != nil {
		return err
	}

	// Static files
	staticPath := path.Join(outPath, "static")
	if err := os.MkdirAll(staticPath, 0777); err != nil {
		return err
	}
	if err := copyFS(staticPath, s.staticFS); err != nil {
		return err
	}

	// Content
	return fs.WalkDir(s.content, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip hidden files and directories
		if len(path.Base(p)) > 1 && path.Base(p)[0] == '.' {
			return nil
		}

		filePath := path.Join(outPath, p)
		if d.IsDir() {
			if err := os.MkdirAll(filePath, 0777); err != nil {
				return err
			}
			return nil
		}
		file, err := s.content.Open(p)
		if err != nil {
			return fs.SkipDir
		}

		out, name, err := s.getFileOutput(path.Base(p), file)
		if err != nil {
			return err
		}

		dir, _ := path.Split(filePath)
		filePath = path.Join(dir, name)

		f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, out)
		err1 := f.Close()
		if err == nil && err1 != nil {
			err = err1
		}
		if err != nil {
			return err
		}

		return nil
	})
}

func must(f func() error) func() {
	return func() {
		if err := f(); err != nil {
			panic(err)
		}
	}
}

func (s *YASG) getFileOutput(filename string, r io.Reader) (io.Reader, string, error) {
	s.once.Do(must(s.loadTemplates))

	// Special cases
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
			return nil, filename, err
		}

		t := TemplParams{
			Content:  template.HTML(buf.String()),
			Metadata: meta.Get(ctx),
		}

		if err := s.templ.Execute(&out, t); err != nil {
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

func (s *YASG) GetRouter() (http.Handler, error) {
	// Generate rendered fs to ease stuff
	log.Println("Generating YASG router...")

	dir, err := os.MkdirTemp("", "yasg-*")
	if err != nil {
		return nil, err
	}

	if err := s.Generate(dir); err != nil {
		return nil, err
	}

	return http.FileServer(http.Dir(dir)), nil
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
	//	staticMD.Generate("./out")

	router, err := staticMD.GetRouter()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Listening on port %d\n", cfg.Port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), router); err != nil {
		log.Fatal(err)
	}
}
