// Package lumen implements the Lumen verbs: resize@v1, export@v1, histogram@v1.
package lumen

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// Request is the JSON-on-stdin form.
type Request struct {
	Input  string `json:"input"`
	Output string `json:"output"`
	Args   struct {
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Format  string `json:"format"`
		Quality int    `json:"quality"`
	} `json:"args"`
}

type result map[string]any

func fail(stdout, stderr io.Writer, err error, detail string) int {
	fmt.Fprintln(stderr, "lumen:", err)
	json.NewEncoder(stdout).Encode(result{"ok": false, "error": err.Error(), "detail": detail})
	return 1
}

// Run executes lumen with args, stdin and writers, returning the exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	verb, req, err := parse(args, stdin)
	if err != nil {
		return fail(stdout, stderr, err, "usage: lumen <resize|export|histogram> --input F [--output F] ... | lumen --verb NAME < request.json")
	}
	res, err := Do(verb, req)
	if err != nil {
		detail := ""
		if _, ok := err.(ErrUnsupported); ok {
			detail = "supported formats: png, jpeg"
		}
		return fail(stdout, stderr, err, detail)
	}
	res["ok"] = true
	json.NewEncoder(stdout).Encode(res)
	return 0
}

func parse(args []string, stdin io.Reader) (string, Request, error) {
	var req Request
	if len(args) == 0 {
		return "", req, fmt.Errorf("no verb given")
	}
	if args[0] == "--verb" || len(args[0]) > 7 && args[0][:7] == "--verb=" {
		fs := flag.NewFlagSet("lumen", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		verb := fs.String("verb", "", "")
		if err := fs.Parse(args); err != nil {
			return "", req, err
		}
		dec := json.NewDecoder(stdin)
		if err := dec.Decode(&req); err != nil {
			return "", req, fmt.Errorf("bad request JSON: %w", err)
		}
		return *verb, req, nil
	}
	verb := args[0]
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&req.Input, "input", "", "")
	fs.StringVar(&req.Output, "output", "", "")
	fs.IntVar(&req.Args.Width, "width", 0, "")
	fs.IntVar(&req.Args.Height, "height", 0, "")
	fs.StringVar(&req.Args.Format, "format", "", "")
	fs.IntVar(&req.Args.Quality, "quality", 0, "")
	if err := fs.Parse(args[1:]); err != nil {
		return "", req, err
	}
	return verb, req, nil
}

// Do runs one verb. Verb names may carry an @vN suffix.
func Do(verb string, req Request) (result, error) {
	switch verb {
	case "resize", "resize@v1":
		return doResize(req)
	case "export", "export@v1":
		return doExport(req)
	case "histogram", "histogram@v1":
		return doHistogram(req)
	}
	return nil, fmt.Errorf("unknown verb %q", verb)
}

func need(req Request, output bool) error {
	if req.Input == "" {
		return fmt.Errorf("input is required")
	}
	if output && req.Output == "" {
		return fmt.Errorf("output is required")
	}
	return nil
}

func doResize(req Request) (result, error) {
	if err := need(req, true); err != nil {
		return nil, err
	}
	format, err := formatFromPath(req.Output)
	if err != nil {
		return nil, err
	}
	src, _, err := decodeFile(req.Input)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	w, h, err := targetSize(sb.Dx(), sb.Dy(), req.Args.Width, req.Args.Height)
	if err != nil {
		return nil, err
	}
	q := req.Args.Quality
	if q == 0 {
		q = 90
	}
	if err := writeFile(req.Output, scale(src, w, h), format, q); err != nil {
		return nil, err
	}
	return result{"output": req.Output, "width": w, "height": h, "format": format,
		"source_width": sb.Dx(), "source_height": sb.Dy()}, nil
}

func doExport(req Request) (result, error) {
	if err := need(req, true); err != nil {
		return nil, err
	}
	var format string
	var err error
	if req.Args.Format != "" {
		format, err = normFormat(req.Args.Format)
	} else {
		format, err = formatFromPath(req.Output)
	}
	if err != nil {
		return nil, err
	}
	src, _, err := decodeFile(req.Input)
	if err != nil {
		return nil, err
	}
	q := req.Args.Quality
	if q == 0 {
		q = 90
	}
	if err := writeFile(req.Output, src, format, q); err != nil {
		return nil, err
	}
	st, _ := os.Stat(req.Output)
	var size int64
	if st != nil {
		size = st.Size()
	}
	return result{"output": req.Output, "format": format, "bytes": size,
		"event": result{"name": "export.finished", "payload": result{
			"source": req.Input, "output": req.Output, "format": format}}}, nil
}

func doHistogram(req Request) (result, error) {
	if err := need(req, false); err != nil {
		return nil, err
	}
	src, _, err := decodeFile(req.Input)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	h := computeHistogram(src)
	return result{"width": b.Dx(), "height": b.Dy(), "pixels": b.Dx() * b.Dy(),
		"red": h.Red, "green": h.Green, "blue": h.Blue, "alpha": h.Alpha}, nil
}
