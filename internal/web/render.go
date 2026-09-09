package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"strings"
	"time"
)

//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

// renderer holds the parsed templates.
//
// Each page is parsed together with the layout in its own set, so that every
// page can define a block called "content" without the names colliding.
type renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
}

func newRenderer() (*renderer, error) {
	funcs := templateFuncs()

	partials, err := template.New("partials").Funcs(funcs).
		ParseFS(templateFS, "templates/partials/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}

	names, err := fs.Glob(templateFS, "templates/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}

	pages := make(map[string]*template.Template)
	for _, name := range names {
		base := strings.TrimPrefix(name, "templates/")
		if base == "layout.gohtml" {
			continue
		}
		t, err := template.New("layout.gohtml").Funcs(funcs).ParseFS(templateFS,
			"templates/layout.gohtml", "templates/partials/*.gohtml", name)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", base, err)
		}
		pages[base] = t
	}
	return &renderer{pages: pages, partials: partials}, nil
}

// page renders a full page.
func (r *renderer) page(w http.ResponseWriter, req *http.Request, name string, data map[string]any) {
	t, ok := r.pages[name]
	if !ok {
		http.Error(w, "unknown template "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		// Part of the response is already written, so reporting the failure to
		// the log is all that remains.
		fmt.Fprintf(w, "<!-- render error: %v -->", err)
	}
}

// partial renders a named fragment, which is what htmx swaps into a page.
func (r *renderer) partial(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.partials.ExecuteTemplate(w, name, data); err != nil {
		fmt.Fprintf(w, "<!-- render error: %v -->", err)
	}
}

// staticHandler serves the vendored assets. They are embedded rather than
// fetched from a content network so the tracker works on a private network with
// no outbound access.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: static assets missing: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.FileServerFS(sub).ServeHTTP(w, r)
	})
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"duration":   formatDuration,
		"clock":      formatClock,
		"datetime":   formatDateTime,
		"round1":     func(v float64) string { return fmt.Sprintf("%.1f", v) },
		"round2":     func(v float64) string { return fmt.Sprintf("%.2f", v) },
		"km":         func(m float64) string { return fmt.Sprintf("%.2f", m/1000) },
		"pct":        func(v float64) string { return fmt.Sprintf("%.0f%%", v*100) },
		"weekday":    weekdayName,
		"hourLabel":  func(h int) string { return fmt.Sprintf("%02d:00", h) },
		"heatColour": heatColour,
		"add":        func(a, b int) int { return a + b },
		"json":       jsonValue,
		"dict":       dict,
		"hasData":    func(n int) bool { return n > 0 },
	}
}

// formatDuration renders seconds the way a driver would say them.
func formatDuration(seconds float64) string {
	if seconds <= 0 {
		return "—"
	}
	d := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m == 0 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", m, s)
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("15:04:05")
}

func formatDateTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

func weekdayName(d int) string {
	return time.Weekday(d % 7).String()
}

// heatColour maps a ratio against the best observed time onto a colour, so that
// a stretch that is reliably slow at a given hour stands out.
//
// A ratio of one is the fastest the stretch is ever driven; the scale saturates
// at twice that, beyond which the exact shade stops carrying information.
func heatColour(ratio float64) template.CSS {
	if ratio <= 0 {
		return template.CSS("var(--surface-2)")
	}
	t := math.Min(math.Max((ratio-1)/1.0, 0), 1)
	// Interpolate through hue from green to red in a perceptually even space.
	hue := 140 * (1 - t)
	return template.CSS(fmt.Sprintf("hsl(%.0f 62%% 45%%)", hue))
}

// jsonValue embeds a value in a script block. The template package escapes it
// for that context, so it is safe for the data being rendered here.
func jsonValue(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}

func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs an even number of arguments, got %d", len(pairs))
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %d is not a string", i)
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}
