package i18n

// Block is the page's #timlee-i18n data block: the messages script may write,
// for one locale, parsed here so the browser never parses ICU syntax
// (docs/I18N.md §5). web/assets/ts/i18n.ts reads it.
//
// A message is an array of parts. A part is text, printed as it is, or an
// array:
//
//	["name"]                                         the argument
//	["#"]                                            the innermost plural's number
//	["name", "plural", {"=0": […], "one": […], "other": […]}]
//	["name", "select", {"keyword": […], "other": […]}]
//
// Tags keep only their content, because script writes plain text.
type Block struct {
	Lang string        `json:"lang"`
	Intl string        `json:"intl"`
	Dir  string        `json:"dir"`
	M    map[Key][]any `json:"m"`
}

// Block returns keys' messages for script, falling back as T does. A key no
// catalog holds is sent as ⟦key⟧, as the server would print it.
func (l *Localizer) Block(keys ...Key) Block {
	b := Block{Lang: l.loc.Lang, Intl: l.loc.Intl, Dir: l.loc.Dir, M: make(map[Key][]any, len(keys))}
	for _, k := range keys {
		if m := l.lookup(k); m != nil {
			b.M[k] = blockParts(m.nodes)
		} else {
			b.M[k] = []any{missing(k)}
		}
	}
	return b
}

func blockParts(nodes []node) []any {
	parts := make([]any, 0, len(nodes))
	for i := range nodes {
		parts = appendBlockPart(parts, &nodes[i])
	}
	return parts
}

func appendBlockPart(parts []any, n *node) []any {
	switch n.kind {
	case textNode:
		// Text next to text joins, so a tag left out leaves no seam.
		if last := len(parts) - 1; last >= 0 {
			if s, ok := parts[last].(string); ok {
				parts[last] = s + n.text
				return parts
			}
		}
		return append(parts, n.text)
	case argNode:
		return append(parts, []any{n.text})
	case poundNode:
		return append(parts, []any{"#"})
	case pluralNode, selectNode:
		kind := "plural"
		if n.kind == selectNode {
			kind = "select"
		}
		branches := make(map[string][]any, len(n.branches))
		for i := range n.branches {
			branches[n.branches[i].key] = blockParts(n.branches[i].nodes)
		}
		return append(parts, []any{n.text, kind, branches})
	case tagNode:
		for i := range n.children {
			parts = appendBlockPart(parts, &n.children[i])
		}
	}
	return parts
}
