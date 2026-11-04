package i18n

import (
	"math"
	"strings"
	"unicode/utf8"
)

// The pseudo-locales are generated from the English catalog at Load, for
// tests only (TIMLEE_PSEUDO_LOCALES; production refuses them). Nobody
// translates them and no file holds them. They show, without a translator,
// text that is not localized, truncated or concatenated, layouts that do not
// survive longer text or a right-to-left page, and data that is not
// isolated:
//
//   - en-XA accents every ASCII letter and pads each sentence inside
//     brackets, "Sign in" → "[Šîĝñ îñ·······]": an unbracketed string was
//     never localized, a missing "]" was cut off, "][" is two messages glued
//     into one sentence.
//   - ar-XB wraps each run of text in RIGHT-TO-LEFT OVERRIDE … POP
//     DIRECTIONAL FORMATTING, so Latin letters run right to left as Arabic
//     would, and each argument in LEFT-TO-RIGHT ISOLATE … POP DIRECTIONAL
//     ISOLATE, so data still reads normally. It formats as Arabic does and
//     picks plural branches by Arabic's six categories.

const (
	rlo = "\u202e" // RIGHT-TO-LEFT OVERRIDE
	pdf = "\u202c" // POP DIRECTIONAL FORMATTING
	lri = "\u2066" // LEFT-TO-RIGHT ISOLATE
)

// accents maps each ASCII letter to an accented look-alike.
var accents = func() map[rune]rune {
	const plain = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const fancy = "åƀçðéƒĝĥîĵķļɱñöþǫŕšţûṽŵẋýžÅƁÇÐÉƑĜĤÎĴĶĻṀÑÖÞǪŔŠŢÛṼŴẊÝŽ"
	m := make(map[rune]rune, len(plain))
	f := []rune(fancy)
	for i, r := range plain {
		m[r] = f[i]
	}
	return m
}()

// pseudoCatalog generates a pseudo-locale's messages from English.
func pseudoCatalog(loc *Locale, source catalog) catalog {
	transform := expand
	if loc.Dir == "rtl" {
		transform = mirror
	}
	f := formatters[cldrID(loc.Intl)]
	cat := make(catalog, len(source))
	for k, src := range source {
		m := &message{nodes: transform(src.nodes), shape: src.shape}
		if m.shape.static() {
			m.static = true
			m.plain, m.html = staticText(f, m.nodes)
		}
		cat[k] = m
	}
	return cat
}

// expand accents and pads a message. A plural or select that is the whole
// message holds a full sentence in each branch, so each branch is padded on
// its own.
func expand(nodes []node) []node {
	if len(nodes) == 1 && (nodes[0].kind == pluralNode || nodes[0].kind == selectNode) {
		n := nodes[0]
		n.branches = mapBranches(n.branches, expand)
		return []node{n}
	}
	accented := accent(nodes)
	length := textLength(accented)
	out := make([]node, 0, len(accented)+2)
	out = append(out, node{kind: textNode, text: "["})
	out = append(out, accented...)
	return append(out, node{kind: textNode, text: strings.Repeat("·", padding(length)) + "]"})
}

// padding is how much longer a sentence of n characters is made: short
// strings grow most in translation (W3C, "Text size in translation").
func padding(n int) int {
	factor := 1.35
	switch {
	case n <= 10:
		factor = 2.0
	case n <= 20:
		factor = 1.8
	case n <= 30:
		factor = 1.6
	case n <= 50:
		factor = 1.5
	}
	return int(math.Ceil(float64(n)*factor)) - n
}

func accent(nodes []node) []node {
	out := make([]node, len(nodes))
	for i, n := range nodes {
		switch n.kind {
		case textNode:
			n.text = strings.Map(func(r rune) rune {
				if a, ok := accents[r]; ok {
					return a
				}
				return r
			}, n.text)
		case pluralNode, selectNode:
			n.branches = mapBranches(n.branches, accent)
		case tagNode:
			n.children = accent(n.children)
		}
		out[i] = n
	}
	return out
}

func textLength(nodes []node) int {
	total := 0
	for _, n := range nodes {
		switch n.kind {
		case textNode:
			total += utf8.RuneCountInString(n.text)
		case pluralNode, selectNode:
			for _, b := range n.branches {
				total += textLength(b.nodes)
			}
		case tagNode:
			total += textLength(n.children)
		}
	}
	return total
}

// mirror overrides every run of text to right-to-left and isolates every
// argument left-to-right.
func mirror(nodes []node) []node {
	out := make([]node, 0, len(nodes))
	for _, n := range nodes {
		switch n.kind {
		case textNode:
			n.text = rlo + n.text + pdf
		case argNode, poundNode:
			out = append(out, node{kind: textNode, text: lri}, n, node{kind: textNode, text: pdi})
			continue
		case pluralNode, selectNode:
			n.branches = mapBranches(n.branches, mirror)
		case tagNode:
			n.children = mirror(n.children)
		}
		out = append(out, n)
	}
	return out
}

func mapBranches(branches []branch, f func([]node) []node) []branch {
	out := make([]branch, len(branches))
	for i, b := range branches {
		b.nodes = f(b.nodes)
		out[i] = b
	}
	return out
}
