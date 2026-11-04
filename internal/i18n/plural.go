package i18n

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rahulguptaio/timlee/internal/i18n/cldr"
)

// Category is a CLDR plural category.
type Category uint8

// The plural categories, in CLDR order.
const (
	Zero  Category = cldr.Zero
	One   Category = cldr.One
	Two   Category = cldr.Two
	Few   Category = cldr.Few
	Many  Category = cldr.Many
	Other Category = cldr.Other
)

// String is the CLDR keyword: "one", "few".
func (c Category) String() string {
	if int(c) < len(cldr.CategoryNames) {
		return cldr.CategoryNames[c]
	}
	return "Category(" + strconv.Itoa(int(c)) + ")"
}

// categoryOf parses a CLDR keyword.
func categoryOf(name string) (Category, bool) {
	for i, n := range cldr.CategoryNames {
		if n == name {
			return Category(i), true
		}
	}
	return 0, false
}

// bigMod is the modulus large operands are reduced by. Every divisor a CLDR
// rule uses (10, 100, 1000, 1000000) divides it, so i % 100 stays exact for
// integers far beyond what an int64 holds.
const bigMod = 1_000_000_000_000_000_000

// operands are the CLDR plural operands of a formatted number
// (https://unicode.org/reports/tr35/tr35-numbers.html#Operands). The integer
// and fraction digits are kept modulo bigMod with a flag for the overflow, so
// equality stays exact and every rule's modulus still applies.
type operands struct {
	i, f       uint64 // integer digits and visible fraction digits, mod bigMod
	iBig, fBig bool   // the true value exceeds bigMod
	t          uint64 // f without trailing zeros
	v, w       int    // visible fraction digits, with and without trailing zeros
}

// intOperands are the operands of an integer.
func intOperands(n int64) operands {
	u := uint64(n)
	if n < 0 {
		u = -u
	}
	return operands{i: u % bigMod, iBig: u >= bigMod}
}

// pluralRules are one language's cardinal rules: a condition per category
// except Other, which is what none of them match.
type pluralRules struct {
	conds [Other][]andCondition
	has   [Other]bool
}

// andCondition is relations joined by "and"; a rule is such groups joined by
// "or".
type andCondition []relation

type relation struct {
	operand byte // n i v w f t c e
	mod     uint64
	not     bool
	ranges  [][2]uint64
}

func parsePluralRules(src [cldr.Categories]string) (*pluralRules, error) {
	r := &pluralRules{}
	for c := Zero; c < Other; c++ {
		if src[c] == "" {
			continue
		}
		cond, err := parseCondition(src[c])
		if err != nil {
			return nil, fmt.Errorf("plural rule %s %q: %w", c, src[c], err)
		}
		r.conds[c], r.has[c] = cond, true
	}
	return r, nil
}

// parseCondition reads the modern CLDR rule syntax: "i = 1 and v = 0",
// "n % 100 = 3..10", "e = 0 and i != 0 and i % 1000000 = 0 or e != 0..5".
func parseCondition(s string) ([]andCondition, error) {
	var out []andCondition
	for _, or := range strings.Split(s, " or ") {
		var and andCondition
		for _, rel := range strings.Split(or, " and ") {
			r, err := parseRelation(strings.TrimSpace(rel))
			if err != nil {
				return nil, err
			}
			and = append(and, r)
		}
		out = append(out, and)
	}
	return out, nil
}

func parseRelation(s string) (relation, error) {
	var r relation
	expr, values, ok := strings.Cut(s, "!=")
	if ok {
		r.not = true
	} else if expr, values, ok = strings.Cut(s, "="); !ok {
		return r, fmt.Errorf("relation %q has no = or !=", s)
	}
	expr = strings.TrimSpace(expr)
	operand, mod, hasMod := strings.Cut(expr, "%")
	operand = strings.TrimSpace(operand)
	if len(operand) != 1 || strings.IndexByte("nivwftce", operand[0]) < 0 {
		return r, fmt.Errorf("unknown operand %q", operand)
	}
	r.operand = operand[0]
	if hasMod {
		m, err := strconv.ParseUint(strings.TrimSpace(mod), 10, 64)
		if err != nil || m == 0 || bigMod%m != 0 {
			return r, fmt.Errorf("unsupported modulus %q", mod)
		}
		r.mod = m
	}
	for _, part := range strings.Split(values, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "..")
		a, err := strconv.ParseUint(lo, 10, 64)
		if err != nil {
			return r, fmt.Errorf("value %q: %w", part, err)
		}
		b := a
		if isRange {
			if b, err = strconv.ParseUint(hi, 10, 64); err != nil || b < a {
				return r, fmt.Errorf("range %q", part)
			}
		}
		r.ranges = append(r.ranges, [2]uint64{a, b})
	}
	return r, nil
}

// category picks the plural category of a number.
func (r *pluralRules) category(o operands) Category {
	for c := Zero; c < Other; c++ {
		if r.has[c] && matches(r.conds[c], o) {
			return c
		}
	}
	return Other
}

func matches(cond []andCondition, o operands) bool {
	for _, and := range cond {
		ok := true
		for i := range and {
			if !and[i].holds(o) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (r *relation) holds(o operands) bool {
	var v uint64
	big, integral := false, true
	switch r.operand {
	case 'n':
		// n is the absolute value: only an integral one can equal a value.
		v, big, integral = o.i, o.iBig, o.t == 0
	case 'i':
		v, big = o.i, o.iBig
	case 'v':
		v = uint64(o.v)
	case 'w':
		v = uint64(o.w)
	case 'f':
		v, big = o.f, o.fBig
	case 't':
		v, big = o.t, o.fBig
	case 'c', 'e':
		// The compact exponent. Intl's PluralRules never sees one, and the
		// compact formatter chooses its pattern before applying it.
		v = 0
	}
	in := false
	if integral {
		if r.mod != 0 {
			v, big = v%r.mod, false
		}
		if !big {
			for _, rg := range r.ranges {
				if v >= rg[0] && v <= rg[1] {
					in = true
					break
				}
			}
		}
	}
	return in != r.not
}
