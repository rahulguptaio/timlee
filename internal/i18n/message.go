package i18n

import (
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"
)

// The message syntax is a subset of ICU MessageFormat 1, the format of the
// ARB catalogs (docs/I18N.md §3):
//
//	message  := ( text | arg | plural | select | tag )*
//	arg      := "{" name "}"
//	plural   := "{" name "," "plural" "," branch+ "}"   "#" prints the number
//	select   := "{" name "," "select" "," branch+ "}"   "other" is required
//	branch   := ( "=" digits | category | keyword ) "{" message "}"
//	tag      := "<" t ">" message "</" t ">"            t: b em code kbd link link2…link9
//
// An ASCII apostrophe quotes only before { } # < or another apostrophe ('' is
// one apostrophe), as in ICU 4.8 and later. A "<" not followed by a letter or
// "/" is text. Messages are parsed once, at Load; rendering walks the tree.

// Bidi isolates wrapped around string arguments in plain text.
const (
	fsi = "\u2068" // FIRST STRONG ISOLATE
	pdi = "\u2069" // POP DIRECTIONAL ISOLATE
)

type nodeKind uint8

const (
	textNode nodeKind = iota
	argNode
	pluralNode
	selectNode
	poundNode
	tagNode
)

// node is one piece of a parsed message.
type node struct {
	kind nodeKind
	// text is the literal of a text node, the argument of an arg, plural or
	// select node, or the name of a tag.
	text     string
	branches []branch // plural and select
	children []node   // tag content
}

type branch struct {
	key   string // "one", "=0", a select keyword
	exact bool   // key is "=N"
	n     int64  // N
	nodes []node
}

// allowedTags are the rich-text tags a message may use, without attributes.
var allowedTags = map[string]bool{
	"b": true, "em": true, "code": true, "kbd": true, "link": true,
	"link2": true, "link3": true, "link4": true, "link5": true,
	"link6": true, "link7": true, "link8": true, "link9": true,
}

// argKind is how a message uses an argument.
type argKind uint8

const (
	simpleArg argKind = iota + 1 // {name}
	pluralArg                    // {name, plural, …}
	selectArg                    // {name, select, …}
	hrefArg                      // the href of <link>
)

// parseMessage parses one message.
func parseMessage(src string) ([]node, error) {
	p := &parser{src: src}
	nodes, err := p.nodes(false, "")
	if err != nil {
		return nil, fmt.Errorf("at byte %d: %w", p.pos, err)
	}
	return nodes, nil
}

type parser struct {
	src    string
	pos    int
	plural int // depth of plural branches, where # is the number
}

// nodes reads until the end, a "}" closing a branch, or the closing tag of
// tag.
func (p *parser) nodes(inBranch bool, tag string) ([]node, error) {
	var out []node
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, node{kind: textNode, text: text.String()})
			text.Reset()
		}
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '{':
			flush()
			n, err := p.argument()
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		case c == '}':
			if !inBranch || tag != "" {
				return nil, errors.New("unmatched }")
			}
			flush()
			return out, nil
		case c == '<' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			p.pos += 2
			name := p.ident()
			if name == "" || !p.consume('>') {
				return nil, errors.New("malformed closing tag")
			}
			if name != tag {
				return nil, fmt.Errorf("</%s> closes nothing open", name)
			}
			flush()
			return out, nil
		case c == '<' && p.pos+1 < len(p.src) && isLetter(p.src[p.pos+1]):
			p.pos++
			name := p.ident()
			if !p.consume('>') {
				return nil, fmt.Errorf("tag <%s must close with > and has no attributes", name)
			}
			if !allowedTags[name] {
				return nil, fmt.Errorf("unknown tag <%s>", name)
			}
			flush()
			children, err := p.nodes(false, name)
			if err != nil {
				return nil, err
			}
			out = append(out, node{kind: tagNode, text: name, children: children})
		case c == '#' && p.plural > 0:
			flush()
			out = append(out, node{kind: poundNode})
			p.pos++
		case c == '\'':
			p.quote(&text)
		default:
			text.WriteByte(c)
			p.pos++
		}
	}
	switch {
	case tag != "":
		return nil, fmt.Errorf("<%s> is not closed", tag)
	case inBranch:
		return nil, errors.New("branch is not closed")
	}
	flush()
	return out, nil
}

// quote handles an apostrophe at p.pos.
func (p *parser) quote(text *strings.Builder) {
	p.pos++
	if p.pos < len(p.src) && p.src[p.pos] == '\'' {
		text.WriteByte('\'')
		p.pos++
		return
	}
	if p.pos >= len(p.src) || !strings.ContainsRune("{}#<", rune(p.src[p.pos])) {
		text.WriteByte('\'')
		return
	}
	// Quoted literal text up to the next lone apostrophe, or the end.
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\'' {
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
				text.WriteByte('\'')
				p.pos += 2
				continue
			}
			p.pos++
			return
		}
		text.WriteByte(c)
		p.pos++
	}
}

// argument reads "{name}", "{name, plural, …}" or "{name, select, …}".
func (p *parser) argument() (node, error) {
	p.pos++ // {
	p.space()
	name := p.ident()
	if name == "" {
		return node{}, errors.New("argument without a name")
	}
	p.space()
	if p.consume('}') {
		return node{kind: argNode, text: name}, nil
	}
	if !p.consume(',') {
		return node{}, fmt.Errorf("argument %q: expected } or ,", name)
	}
	p.space()
	n := node{text: name}
	switch kind := p.ident(); kind {
	case "plural":
		n.kind = pluralNode
	case "select":
		n.kind = selectNode
	default:
		return node{}, fmt.Errorf("argument %q: unsupported type %q (only plural and select)", name, kind)
	}
	p.space()
	if !p.consume(',') {
		return node{}, fmt.Errorf("argument %q: expected , after the type", name)
	}
	seen := map[string]bool{}
	for {
		p.space()
		if p.consume('}') {
			break
		}
		var b branch
		if p.consume('=') {
			start := p.pos
			for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
				p.pos++
			}
			v, err := strconv.ParseInt(p.src[start:p.pos], 10, 64)
			if err != nil || n.kind != pluralNode {
				return node{}, fmt.Errorf("argument %q: bad exact branch", name)
			}
			b.key, b.exact, b.n = "="+p.src[start:p.pos], true, v
		} else {
			b.key = p.ident()
			if b.key == "" {
				return node{}, fmt.Errorf("argument %q: expected a branch keyword", name)
			}
			if n.kind == pluralNode {
				if b.key == "offset" {
					return node{}, fmt.Errorf("argument %q: offset is not supported", name)
				}
				if _, ok := categoryOf(b.key); !ok {
					return node{}, fmt.Errorf("argument %q: %q is not a plural category", name, b.key)
				}
			}
		}
		if seen[b.key] {
			return node{}, fmt.Errorf("argument %q: branch %q twice", name, b.key)
		}
		seen[b.key] = true
		p.space()
		if !p.consume('{') {
			return node{}, fmt.Errorf("argument %q: branch %q needs a {message}", name, b.key)
		}
		if n.kind == pluralNode {
			p.plural++
		}
		nodes, err := p.nodes(true, "")
		if n.kind == pluralNode {
			p.plural--
		}
		if err != nil {
			return node{}, err
		}
		p.pos++ // }
		b.nodes = nodes
		n.branches = append(n.branches, b)
	}
	if !seen["other"] {
		return node{}, fmt.Errorf("argument %q: no other branch", name)
	}
	return n, nil
}

func (p *parser) ident() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if !isLetter(c) && c != '_' && (c < '0' || c > '9' || p.pos == start) {
			break
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) space() {
	for p.pos < len(p.src) && strings.IndexByte(" \t\r\n", p.src[p.pos]) >= 0 {
		p.pos++
	}
}

func (p *parser) consume(c byte) bool {
	if p.pos < len(p.src) && p.src[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

/* -------------------------------------------------------------- analysis -- */

// shape is what a message needs from its caller, compared between a
// translation and its English source.
type shape struct {
	args map[string]argKind
	tags []string // sorted, with repeats
	// categories are the plural categories the message branches on.
	categories map[Category]bool
}

func shapeOf(nodes []node) shape {
	s := shape{args: map[string]argKind{}, categories: map[Category]bool{}}
	s.walk(nodes)
	sort.Strings(s.tags)
	return s
}

func (s *shape) walk(nodes []node) {
	for i := range nodes {
		n := &nodes[i]
		switch n.kind {
		case argNode:
			if s.args[n.text] == 0 {
				s.args[n.text] = simpleArg
			}
		case pluralNode, selectNode:
			if n.kind == pluralNode {
				s.args[n.text] = pluralArg
			} else if s.args[n.text] != pluralArg {
				s.args[n.text] = selectArg
			}
			for _, b := range n.branches {
				if c, ok := categoryOf(b.key); ok && n.kind == pluralNode {
					s.categories[c] = true
				}
				s.walk(b.nodes)
			}
		case tagNode:
			s.tags = append(s.tags, n.text)
			if strings.HasPrefix(n.text, "link") {
				s.args[n.text] = hrefArg
			}
			s.walk(n.children)
		}
	}
}

// static reports whether a message renders the same for every call: no
// arguments, so its text can be computed once.
func (s *shape) static() bool { return len(s.args) == 0 }

// compatible reports why a translation's shape does not fit its source's.
// A number may be printed plainly in one language and pluralized in the
// other (Japanese has no plural forms), but every argument and tag must be
// the same.
func (s shape) compatible(src shape) error {
	for name, kind := range src.args {
		got, ok := s.args[name]
		switch {
		case !ok:
			return fmt.Errorf("argument %q is missing", name)
		case got != kind && !(got == simpleArg && kind == pluralArg || got == pluralArg && kind == simpleArg):
			return fmt.Errorf("argument %q is used differently than in English", name)
		}
	}
	for name := range s.args {
		if _, ok := src.args[name]; !ok {
			return fmt.Errorf("argument %q is not in the English message", name)
		}
	}
	if strings.Join(s.tags, ",") != strings.Join(src.tags, ",") {
		return fmt.Errorf("tags %v differ from English %v", s.tags, src.tags)
	}
	return nil
}

/* ------------------------------------------------------------- rendering -- */

// Formatted is text this package already formatted for the locale, such as
// a number or a date. A message prints it as it is, where it would isolate a
// plain string argument as text of unknown direction.
type Formatted string

// args are the arguments of one render: name/value pairs, plus the count
// TN binds.
type args struct {
	pairs    []any
	count    int64
	hasCount bool
}

func (a *args) get(name string) (any, bool) {
	if a.hasCount && name == "count" {
		return a.count, true
	}
	for i := 0; i+1 < len(a.pairs); i += 2 {
		if k, ok := a.pairs[i].(string); ok && k == name {
			return a.pairs[i+1], true
		}
	}
	return nil, false
}

// integer looks an integer argument up without boxing the bound count.
func (a *args) integer(name string) (int64, bool) {
	if a.hasCount && name == "count" {
		return a.count, true
	}
	v, ok := a.get(name)
	if !ok {
		return 0, false
	}
	return toInt64(v)
}

func (a *args) check() error {
	if len(a.pairs)%2 != 0 {
		return fmt.Errorf("arguments must be name/value pairs, got %d values", len(a.pairs))
	}
	for i := 0; i < len(a.pairs); i += 2 {
		if _, ok := a.pairs[i].(string); !ok {
			return fmt.Errorf("argument name %d is %T, want string", i/2, a.pairs[i])
		}
	}
	return nil
}

// renderer writes a message for one locale, as plain text or as HTML.
type renderer struct {
	f    *formatter
	html bool
	args args
	err  error
	// number is what # prints: the innermost plural's value.
	number    decimal
	hasNumber bool
}

func (r *renderer) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// appendNodes writes nodes.
func (r *renderer) appendNodes(b []byte, nodes []node) []byte {
	for i := range nodes {
		n := &nodes[i]
		switch n.kind {
		case textNode:
			b = r.appendText(b, n.text)
		case argNode:
			b = r.appendArg(b, n.text)
		case poundNode:
			if r.hasNumber {
				b = r.f.decimal.format(b, &r.number, r.f)
			}
		case pluralNode:
			count, ok := r.args.integer(n.text)
			if !ok {
				v, _ := r.args.get(n.text)
				r.fail(fmt.Errorf("plural argument %q needs an integer, got %T", n.text, v))
				b = append(b, '{')
				b = append(b, n.text...)
				b = append(b, '}')
				continue
			}
			outer, hadOuter := r.number, r.hasNumber
			r.number, r.hasNumber = decimalOfInt(count), true
			b = r.appendNodes(b, r.pluralBranch(n, count))
			r.number, r.hasNumber = outer, hadOuter
		case selectNode:
			v, ok := r.args.get(n.text)
			if !ok {
				r.fail(fmt.Errorf("select argument %q is missing", n.text))
			}
			b = r.appendNodes(b, selectBranch(n, toString(v)))
		case tagNode:
			b = r.appendTag(b, n)
		}
	}
	return b
}

// pluralBranch picks an exact match first, then the number's category, then
// other.
func (r *renderer) pluralBranch(n *node, count int64) []node {
	for i := range n.branches {
		if b := &n.branches[i]; b.exact && b.n == count {
			return b.nodes
		}
	}
	key := r.f.plural.category(intOperands(count)).String()
	var other []node
	for i := range n.branches {
		switch b := &n.branches[i]; b.key {
		case key:
			return b.nodes
		case "other":
			other = b.nodes
		}
	}
	return other
}

func selectBranch(n *node, value string) []node {
	var other []node
	for i := range n.branches {
		switch b := &n.branches[i]; b.key {
		case value:
			return b.nodes
		case "other":
			other = b.nodes
		}
	}
	return other
}

func (r *renderer) appendText(b []byte, s string) []byte {
	if !r.html {
		return append(b, s...)
	}
	return appendEscaped(b, s)
}

func (r *renderer) appendArg(b []byte, name string) []byte {
	v, ok := r.args.get(name)
	if !ok {
		r.fail(fmt.Errorf("argument %q is missing", name))
		b = append(b, '{')
		b = append(b, name...)
		return append(b, '}')
	}
	switch v := v.(type) {
	case Formatted:
		return r.appendText(b, string(v))
	case string:
		// User data until proven otherwise: isolate it so its direction
		// cannot reorder the sentence around it.
		if r.html {
			b = append(b, "<bdi>"...)
			b = appendEscaped(b, v)
			return append(b, "</bdi>"...)
		}
		b = append(b, fsi...)
		b = append(b, v...)
		return append(b, pdi...)
	}
	if n, ok := toInt64(v); ok {
		d := decimalOfInt(n)
		return r.f.decimal.format(b, &d, r.f)
	}
	r.fail(fmt.Errorf("argument %q is %T; pass a string, an integer or a Formatted", name, v))
	return b
}

func (r *renderer) appendTag(b []byte, n *node) []byte {
	if !r.html {
		return r.appendNodes(b, n.children)
	}
	switch n.text {
	case "code":
		// Code reads left to right whatever the page's direction.
		b = append(b, `<code dir="ltr">`...)
	case "b", "em", "kbd":
		b = append(b, '<')
		b = append(b, n.text...)
		b = append(b, '>')
	default: // link, link2…link9
		v, _ := r.args.get(n.text)
		href, ok := v.(string)
		if !ok {
			r.fail(fmt.Errorf("<%s> needs a string argument %q with its href", n.text, n.text))
		}
		b = append(b, `<a href="`...)
		b = appendEscaped(b, safeHref(href))
		b = append(b, `">`...)
	}
	b = r.appendNodes(b, n.children)
	b = append(b, "</"...)
	if strings.HasPrefix(n.text, "link") {
		b = append(b, 'a')
	} else {
		b = append(b, n.text...)
	}
	return append(b, '>')
}

// safeHref passes paths, fragments and web or mail URLs, and replaces
// anything else (javascript:, data:) the way html/template does.
func safeHref(href string) string {
	switch {
	case strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//"),
		strings.HasPrefix(href, "#"),
		strings.HasPrefix(href, "https://"),
		strings.HasPrefix(href, "http://"),
		strings.HasPrefix(href, "mailto:"):
		return href
	}
	return "#ZgotmplZ"
}

// appendEscaped writes s with the five HTML-significant characters escaped,
// as html/template escapes text.
func appendEscaped(b []byte, s string) []byte {
	last := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '&':
			esc = "&amp;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case '"':
			esc = "&#34;"
		case '\'':
			esc = "&#39;"
		default:
			continue
		}
		b = append(b, s[last:i]...)
		b = append(b, esc...)
		last = i + 1
	}
	return append(b, s[last:]...)
}

func toInt64(v any) (int64, bool) {
	switch v := v.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int16:
		return int64(v), true
	case int8:
		return int64(v), true
	case uint:
		return int64(min(uint64(v), 1<<63-1)), true
	case uint64:
		return int64(min(v, 1<<63-1)), true
	case uint32:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint8:
		return int64(v), true
	}
	return 0, false
}

func toString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case Formatted:
		return string(v)
	case nil:
		return ""
	case fmt.Stringer:
		return v.String()
	}
	return fmt.Sprint(v)
}

// staticText renders a message without arguments once, at Load.
func staticText(f *formatter, nodes []node) (string, template.HTML) {
	r := renderer{f: f}
	plain := string(r.appendNodes(nil, nodes))
	r.html = true
	return plain, template.HTML(r.appendNodes(nil, nodes)) //nolint:gosec // escaped by appendNodes
}
