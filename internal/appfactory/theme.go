package appfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Font is one selectable font from packages/design/src/tokens.json.
type Font struct {
	Family string `json:"family"`
	Stack  string `json:"stack"`
	// Imports are the self-hosted packages (or per-weight CSS files) to bundle.
	Imports []string `json:"imports,omitempty"`
	// Vars are extra design-system variables the face needs, such as heading
	// weights for a single-weight display face.
	Vars map[string]string `json:"vars,omitempty"`
}

// Token is one editable design knob.
type Token struct {
	Name  string   `json:"name"`
	Label string   `json:"label"`
	Type  string   `json:"type"` // color | number
	Light string   `json:"light"`
	Dark  string   `json:"dark"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Step  *float64 `json:"step,omitempty"`
}

// ContrastRule requires two color tokens to keep a WCAG contrast ratio.
type ContrastRule struct {
	FG  string  `json:"fg"`
	BG  string  `json:"bg"`
	Min float64 `json:"min"`
}

// Catalog is the design system's editable surface; the editor renders it.
type Catalog struct {
	Fonts    []Font         `json:"fonts"`
	Tokens   []Token        `json:"tokens"`
	Contrast []ContrastRule `json:"contrast"`
}

// Catalog reads the design system's token catalog from the workspace, so the
// editor and the apps share one source of truth.
func (f *Factory) Catalog() (Catalog, error) {
	var c Catalog
	raw, err := os.ReadFile(filepath.Join(f.cfg.Workspace, "packages", "design", "src", "tokens.json"))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("tokens.json: %w", err)
	}
	if len(c.Fonts) == 0 {
		return c, fmt.Errorf("tokens.json: no fonts")
	}
	return c, nil
}

var (
	hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	// Plain decimals only: ParseFloat also accepts NaN, Inf, hex, and underscores.
	decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	// Font imports are written into JavaScript; only package paths are allowed.
	importPath = regexp.MustCompile(`^@?[a-z0-9][a-z0-9._/-]*$`)
	cssVarName = regexp.MustCompile(`^--[a-z0-9-]+$`)
	cssValue   = regexp.MustCompile(`^[A-Za-z0-9 ."',-]+$`)
)

// validateTheme accepts only catalog fonts and tokens with well-formed values,
// because the values are written verbatim into CSS, and refuses color choices
// that would fail the catalog's contrast rules.
func validateTheme(c Catalog, font string, tokens Tokens) (Font, error) {
	var chosen *Font
	for i := range c.Fonts {
		if c.Fonts[i].Family == font {
			chosen = &c.Fonts[i]
		}
	}
	if chosen == nil {
		return Font{}, fmt.Errorf("%w: unknown font %q", ErrInvalid, font)
	}
	for _, imp := range chosen.Imports {
		if !importPath.MatchString(imp) || strings.Contains(imp, "..") {
			return Font{}, fmt.Errorf("tokens.json: font %q has an unsafe import %q", chosen.Family, imp)
		}
	}
	for name, value := range chosen.Vars {
		if !cssVarName.MatchString(name) || !cssValue.MatchString(value) {
			return Font{}, fmt.Errorf("tokens.json: font %q has an unsafe variable %q", chosen.Family, name)
		}
	}

	byName := map[string]Token{}
	for _, t := range c.Tokens {
		byName[t.Name] = t
	}
	for _, scheme := range []map[string]string{tokens.Light, tokens.Dark} {
		for name, value := range scheme {
			t, ok := byName[name]
			if !ok {
				return Font{}, fmt.Errorf("%w: unknown token %q", ErrInvalid, name)
			}
			switch t.Type {
			case "color":
				if !hexColor.MatchString(value) {
					return Font{}, fmt.Errorf("%w: %s must be a #rrggbb color", ErrInvalid, t.Label)
				}
			case "number":
				n, err := strconv.ParseFloat(value, 64)
				if !decimal.MatchString(value) || err != nil || math.IsNaN(n) ||
					(t.Min != nil && n < *t.Min) || (t.Max != nil && n > *t.Max) {
					return Font{}, fmt.Errorf("%w: %s is out of range", ErrInvalid, t.Label)
				}
			default:
				return Font{}, fmt.Errorf("%w: token %q has unsupported type", ErrInvalid, name)
			}
		}
	}

	for _, scheme := range []struct {
		label  string
		values map[string]string
		pick   func(Token) string
	}{
		{"light", tokens.Light, func(t Token) string { return t.Light }},
		{"dark", tokens.Dark, func(t Token) string { return t.Dark }},
	} {
		value := func(name string) string {
			if v, ok := scheme.values[name]; ok {
				return v
			}
			return scheme.pick(byName[name])
		}
		for _, rule := range c.Contrast {
			fg, bg := value(rule.FG), value(rule.BG)
			if !hexColor.MatchString(fg) || !hexColor.MatchString(bg) {
				continue
			}
			if ratio := contrastRatio(fg, bg); ratio < rule.Min {
				return Font{}, fmt.Errorf("%w: in %s mode, %s on %s has contrast %.2f:1; it needs at least %g:1",
					ErrInvalid, scheme.label, byName[rule.FG].Label, byName[rule.BG].Label, ratio, rule.Min)
			}
		}
	}
	return *chosen, nil
}

// contrastRatio is the WCAG 2 contrast ratio of two #rrggbb colors.
func contrastRatio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	channel := func(i int) float64 {
		v, _ := strconv.ParseUint(hex[i:i+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
}

// renderThemeCSS produces src/theme.override.css. Only overrides that differ
// from the catalog defaults are written, so design-system updates still flow
// through to apps that never touched a token.
func renderThemeCSS(c Catalog, font Font, tokens Tokens) string {
	var b strings.Builder
	b.WriteString("/* Generated by Timlee from timlee.json. Edit the theme in the Timlee editor, not here. */\n")
	write := func(selector string, values map[string]string, defaults func(Token) string, extra []string) {
		lines := append([]string(nil), extra...)
		for _, t := range c.Tokens {
			if v, ok := values[t.Name]; ok && v != defaults(t) {
				lines = append(lines, fmt.Sprintf("  %s: %s;", t.Name, v))
			}
		}
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s {\n%s\n}\n", selector, strings.Join(lines, "\n"))
	}
	var fontLines []string
	if font.Family != "" && font.Family != c.Fonts[0].Family {
		fontLines = append(fontLines, "  --timlee-font-body: "+font.Stack+";")
		names := make([]string, 0, len(font.Vars))
		for name := range font.Vars {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fontLines = append(fontLines, fmt.Sprintf("  %s: %s;", name, font.Vars[name]))
		}
	}
	write(":root,\n.wa-light", tokens.Light, func(t Token) string { return t.Light }, fontLines)
	write(".wa-dark", tokens.Dark, func(t Token) string { return t.Dark }, nil)
	return b.String()
}

func renderFontJS(font Font) string {
	s := "// Generated by Timlee from timlee.json: imports the self-hosted font package, if any.\n"
	for _, imp := range font.Imports {
		s += fmt.Sprintf("import '%s';\n", imp)
	}
	return s
}

// writeAppFile replaces one Timlee-owned file inside the app. It goes through
// os.Root and removes the old entry first, so a symlink planted in its place
// is replaced rather than followed out of the app.
func (f *Factory) writeAppFile(slug, rel string, data []byte) error {
	root, err := f.openRoot(slug)
	if err != nil {
		return err
	}
	defer root.Close()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := root.RemoveAll(rel); err != nil {
		return err
	}
	return root.WriteFile(rel, data, 0o644)
}

// writeThemeFiles regenerates the files derived from timlee.json.
func (f *Factory) writeThemeFiles(app App) error {
	c, err := f.Catalog()
	if err != nil {
		return err
	}
	font, err := validateTheme(c, app.Font, app.Tokens)
	if err != nil {
		return err
	}
	if err := f.writeAppFile(app.Slug, "src/theme.override.css", []byte(renderThemeCSS(c, font, app.Tokens))); err != nil {
		return err
	}
	return f.writeAppFile(app.Slug, "src/font.js", []byte(renderFontJS(font)))
}

func (f *Factory) writeMeta(app App) error {
	raw, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return err
	}
	return f.writeAppFile(app.Slug, "timlee.json", append(raw, '\n'))
}

// SaveTheme stores the app's font and token overrides, regenerates the theme
// files, rebuilds, and commits.
func (f *Factory) SaveTheme(ctx context.Context, slug, font string, tokens Tokens) (BuildResult, error) {
	c, err := f.Catalog()
	if err != nil {
		return BuildResult{}, err
	}
	if tokens.Light == nil {
		tokens.Light = map[string]string{}
	}
	if tokens.Dark == nil {
		tokens.Dark = map[string]string{}
	}
	if _, err := validateTheme(c, font, tokens); err != nil {
		return BuildResult{}, err
	}
	// Store only real overrides (compared case-insensitively, as colors are),
	// so later design-system changes still reach every token this app never
	// customized.
	for _, t := range c.Tokens {
		if strings.EqualFold(tokens.Light[t.Name], t.Light) {
			delete(tokens.Light, t.Name)
		}
		if strings.EqualFold(tokens.Dark[t.Name], t.Dark) {
			delete(tokens.Dark, t.Name)
		}
	}
	end, err := f.begin(slug)
	if err != nil {
		return BuildResult{}, err
	}
	defer end()
	base, err := f.gitCmd(ctx, "rev-parse", "HEAD")
	if err != nil {
		return BuildResult{}, err
	}
	app, err := f.Get(slug)
	if err != nil {
		return BuildResult{}, err
	}
	app.Font, app.Tokens = font, tokens
	if err := f.writeMeta(app); err != nil {
		return BuildResult{}, err
	}
	if err := f.writeThemeFiles(app); err != nil {
		return BuildResult{}, err
	}
	return f.buildAndCommit(ctx, slug, base, "Update theme")
}
