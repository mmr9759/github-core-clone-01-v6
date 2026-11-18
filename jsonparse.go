package main

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Minimal JSON parser for request bodies. Values decode to:
//   map[string]interface{}  object
//   []interface{}           array
//   string                  string
//   float64                 number
//   bool                    true/false
//   nil                     null

type jsonParser struct {
	data []byte
	pos  int
}

func parseJSON(data []byte) (interface{}, error) {
	p := &jsonParser{data: data}
	p.skipWS()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if p.pos != len(p.data) {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

func (p *jsonParser) skipWS() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) value() (interface{}, error) {
	if p.pos >= len(p.data) {
		return nil, errors.New("unexpected end of JSON")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	default:
		return p.number()
	}
}

func (p *jsonParser) literal(word string, val interface{}) (interface{}, error) {
	if p.pos+len(word) > len(p.data) || string(p.data[p.pos:p.pos+len(word)]) != word {
		return nil, errors.New("bad JSON literal")
	}
	p.pos += len(word)
	return val, nil
}

func (p *jsonParser) number() (interface{}, error) {
	start := p.pos
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			p.pos++
		} else {
			break
		}
	}
	if start == p.pos {
		return nil, errors.New("bad JSON number")
	}
	return strconv.ParseFloat(string(p.data[start:p.pos]), 64)
}

func (p *jsonParser) str() (string, error) {
	if p.data[p.pos] != '"' {
		return "", errors.New("expected string")
	}
	p.pos++
	var b strings.Builder
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '"' {
			return b.String(), nil
		}
		if c == '\\' {
			if p.pos >= len(p.data) {
				return "", errors.New("bad escape")
			}
			esc := p.data[p.pos]
			p.pos++
			switch esc {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				if p.pos+4 > len(p.data) {
					return "", errors.New("bad unicode escape")
				}
				code, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
				if err != nil {
					return "", err
				}
				p.pos += 4
				r := rune(code)
				if utf16.IsSurrogate(r) {
					if p.pos+6 <= len(p.data) && p.data[p.pos] == '\\' && p.data[p.pos+1] == 'u' {
						lo, err := strconv.ParseUint(string(p.data[p.pos+2:p.pos+6]), 16, 32)
						if err == nil {
							r = utf16.DecodeRune(r, rune(lo))
							p.pos += 6
						}
					}
				}
				b.WriteRune(r)
			default:
				return "", errors.New("bad escape")
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", errors.New("unterminated string")
}

func (p *jsonParser) object() (interface{}, error) {
	p.pos++ // {
	obj := map[string]interface{}{}
	p.skipWS()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return obj, nil
	}
	for {
		p.skipWS()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, errors.New("expected object key")
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.skipWS()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, errors.New("expected colon")
		}
		p.pos++
		p.skipWS()
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		obj[key] = val
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, errors.New("unterminated object")
		}
		if p.data[p.pos] == '}' {
			p.pos++
			return obj, nil
		}
		if p.data[p.pos] != ',' {
			return nil, errors.New("expected comma in object")
		}
		p.pos++
	}
}

func (p *jsonParser) array() (interface{}, error) {
	p.pos++ // [
	arr := []interface{}{}
	p.skipWS()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return arr, nil
	}
	for {
		p.skipWS()
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, errors.New("unterminated array")
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return arr, nil
		}
		if p.data[p.pos] != ',' {
			return nil, errors.New("expected comma in array")
		}
		p.pos++
	}
}

// ---------------------------------------------------------------- accessors

func jobj(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func jstr(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func jbool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func jnum(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func jarr(v interface{}) []interface{} {
	if a, ok := v.([]interface{}); ok {
		return a
	}
	return nil
}

// ---------------------------------------------------------------- encoding

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte("0123456789abcdef"[byte(r)>>4])
				b.WriteByte("0123456789abcdef"[byte(r)&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var _ = utf8.RuneLen
