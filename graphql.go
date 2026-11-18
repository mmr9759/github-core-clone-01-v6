package main

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- global ids

func globalID(parts ...string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(parts, ":")))
}

func decodeGlobalID(s string) []string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return strings.Split(string(b), ":")
}

// ---------------------------------------------------------------- tokenizer

type gqlToken struct {
	kind string // name | int | float | string | punct | spread | eof
	val  string
}

func gqlTokenize(src string) ([]gqlToken, error) {
	var toks []gqlToken
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '.' && i+2 < len(src) && src[i+1] == '.' && src[i+2] == '.':
			toks = append(toks, gqlToken{kind: "spread", val: "..."})
			i += 3
		case c == '{' || c == '}' || c == '(' || c == ')' || c == ':' || c == '$' ||
			c == '!' || c == '=' || c == '@' || c == '[' || c == ']' || c == '|' || c == '&':
			toks = append(toks, gqlToken{kind: "punct", val: string(c)})
			i++
		case c == '"':
			j := i + 1
			var b strings.Builder
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' && j+1 < len(src) {
					j++
					switch src[j] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					case '"':
						b.WriteByte('"')
					case '\\':
						b.WriteByte('\\')
					case '/':
						b.WriteByte('/')
					default:
						b.WriteByte(src[j])
					}
					j++
					continue
				}
				b.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, errGQL("unterminated string")
			}
			toks = append(toks, gqlToken{kind: "string", val: b.String()})
			i = j + 1
		case (c >= '0' && c <= '9') || c == '-':
			j := i
			isFloat := false
			for j < len(src) {
				d := src[j]
				if (d >= '0' && d <= '9') || d == '-' || d == '+' || d == 'e' || d == 'E' {
					j++
				} else if d == '.' {
					isFloat = true
					j++
				} else {
					break
				}
			}
			kind := "int"
			if isFloat {
				kind = "float"
			}
			toks = append(toks, gqlToken{kind: kind, val: src[i:j]})
			i = j
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_':
			j := i
			for j < len(src) {
				d := src[j]
				if (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') || (d >= '0' && d <= '9') || d == '_' {
					j++
				} else {
					break
				}
			}
			toks = append(toks, gqlToken{kind: "name", val: src[i:j]})
			i = j
		default:
			return nil, errGQL("unexpected character " + string(c))
		}
	}
	toks = append(toks, gqlToken{kind: "eof"})
	return toks, nil
}

type gqlError struct{ msg string }

func (e *gqlError) Error() string { return e.msg }
func errGQL(msg string) error     { return &gqlError{msg: msg} }

// ---------------------------------------------------------------- AST

type gqlVar struct{ name string }

type gqlVarDef struct {
	defVal interface{}
	hasDef bool
}

type gqlField struct {
	alias    string
	name     string
	args     map[string]interface{}
	sel      *gqlSelSet
	fragName string
	typeCond string
}

type gqlSelSet struct{ fields []*gqlField }

type gqlOp struct {
	opType  string
	name    string
	varDefs map[string]*gqlVarDef
	sel     *gqlSelSet
}

type gqlDoc struct {
	ops   []*gqlOp
	frags map[string]*gqlSelSet
}

type gqlParser struct {
	toks []gqlToken
	pos  int
}

func (p *gqlParser) peek() gqlToken  { return p.toks[p.pos] }
func (p *gqlParser) next() gqlToken  { t := p.toks[p.pos]; p.pos++; return t }
func (p *gqlParser) expect(kind, val string) (gqlToken, error) {
	t := p.next()
	if t.kind != kind || (val != "" && t.val != val) {
		return t, errGQL("expected " + val + " got " + t.val)
	}
	return t, nil
}

func parseGQL(src string) (*gqlDoc, error) {
	toks, err := gqlTokenize(src)
	if err != nil {
		return nil, err
	}
	p := &gqlParser{toks: toks}
	doc := &gqlDoc{frags: map[string]*gqlSelSet{}}
	for p.peek().kind != "eof" {
		t := p.peek()
		if t.kind == "name" && t.val == "fragment" {
			p.next()
			nameTok := p.next()
			if nameTok.kind != "name" {
				return nil, errGQL("fragment needs a name")
			}
			if _, err := p.expect("name", "on"); err != nil {
				return nil, err
			}
			if _, err := p.expect("name", ""); err != nil {
				// type name consumed below
			}
			sel, err := p.selSet()
			if err != nil {
				return nil, err
			}
			doc.frags[nameTok.val] = sel
			continue
		}
		op := &gqlOp{opType: "query", varDefs: map[string]*gqlVarDef{}}
		if t.kind == "name" && (t.val == "query" || t.val == "mutation" || t.val == "subscription") {
			p.next()
			op.opType = t.val
			if p.peek().kind == "name" {
				op.name = p.next().val
			}
		}
		if p.peek().kind == "punct" && p.peek().val == "(" {
			if err := p.varDefs(op); err != nil {
				return nil, err
			}
		}
		// directives on the operation (e.g. @cached) — skip
		for p.peek().kind == "punct" && p.peek().val == "@" {
			p.next()
			p.next() // directive name
			if p.peek().kind == "punct" && p.peek().val == "(" {
				if _, err := p.args(); err != nil {
					return nil, err
				}
			}
		}
		sel, err := p.selSet()
		if err != nil {
			return nil, err
		}
		op.sel = sel
		doc.ops = append(doc.ops, op)
	}
	return doc, nil
}

func (p *gqlParser) varDefs(op *gqlOp) error {
	if _, err := p.expect("punct", "("); err != nil {
		return err
	}
	for {
		if p.peek().kind == "punct" && p.peek().val == ")" {
			p.next()
			return nil
		}
		if _, err := p.expect("punct", "$"); err != nil {
			return err
		}
		nameTok := p.next()
		if nameTok.kind != "name" {
			return errGQL("variable needs a name")
		}
		if _, err := p.expect("punct", ":"); err != nil {
			return err
		}
		// type: consume name / [ ] ! tokens
		for {
			t := p.peek()
			if t.kind == "name" || (t.kind == "punct" && (t.val == "[" || t.val == "]" || t.val == "!")) {
				p.next()
				continue
			}
			break
		}
		def := &gqlVarDef{}
		if p.peek().kind == "punct" && p.peek().val == "=" {
			p.next()
			v, err := p.value()
			if err != nil {
				return err
			}
			def.defVal = v
			def.hasDef = true
		}
		op.varDefs[nameTok.val] = def
	}
}

func (p *gqlParser) selSet() (*gqlSelSet, error) {
	if _, err := p.expect("punct", "{"); err != nil {
		return nil, err
	}
	sel := &gqlSelSet{}
	for {
		t := p.peek()
		if t.kind == "punct" && t.val == "}" {
			p.next()
			return sel, nil
		}
		f, err := p.field()
		if err != nil {
			return nil, err
		}
		sel.fields = append(sel.fields, f)
	}
}

func (p *gqlParser) field() (*gqlField, error) {
	t := p.peek()
	if t.kind == "spread" {
		p.next()
		nt := p.peek()
		if nt.kind == "name" && nt.val == "on" {
			p.next()
			cond := p.next()
			if cond.kind != "name" {
				return nil, errGQL("inline fragment needs a type")
			}
			sel, err := p.selSet()
			if err != nil {
				return nil, err
			}
			return &gqlField{typeCond: cond.val, sel: sel}, nil
		}
		if nt.kind != "name" {
			return nil, errGQL("fragment spread needs a name")
		}
		p.next()
		return &gqlField{fragName: nt.val}, nil
	}
	if t.kind != "name" {
		return nil, errGQL("expected field name, got " + t.val)
	}
	p.next()
	f := &gqlField{name: t.val}
	if p.peek().kind == "punct" && p.peek().val == ":" {
		p.next()
		f.alias = f.name
		nt := p.next()
		if nt.kind != "name" {
			return nil, errGQL("expected field name after alias")
		}
		f.name = nt.val
	}
	if p.peek().kind == "punct" && p.peek().val == "(" {
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		f.args = args
	}
	// directives on the field — skip
	for p.peek().kind == "punct" && p.peek().val == "@" {
		p.next()
		p.next()
		if p.peek().kind == "punct" && p.peek().val == "(" {
			if _, err := p.args(); err != nil {
				return nil, err
			}
		}
	}
	if p.peek().kind == "punct" && p.peek().val == "{" {
		sel, err := p.selSet()
		if err != nil {
			return nil, err
		}
		f.sel = sel
	}
	return f, nil
}

func (p *gqlParser) args() (map[string]interface{}, error) {
	if _, err := p.expect("punct", "("); err != nil {
		return nil, err
	}
	args := map[string]interface{}{}
	for {
		t := p.peek()
		if t.kind == "punct" && t.val == ")" {
			p.next()
			return args, nil
		}
		if t.kind != "name" {
			return nil, errGQL("expected argument name")
		}
		p.next()
		if _, err := p.expect("punct", ":"); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		args[t.val] = v
	}
}

func (p *gqlParser) value() (interface{}, error) {
	t := p.next()
	switch t.kind {
	case "string":
		return t.val, nil
	case "int":
		n, _ := strconv.Atoi(t.val)
		return n, nil
	case "float":
		f, _ := strconv.ParseFloat(t.val, 64)
		return f, nil
	case "name":
		switch t.val {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		default:
			return t.val, nil // enum value
		}
	case "punct":
		switch t.val {
		case "$":
			nt := p.next()
			if nt.kind != "name" {
				return nil, errGQL("expected variable name")
			}
			return gqlVar{name: nt.val}, nil
		case "[":
			var arr []interface{}
			for {
				if p.peek().kind == "punct" && p.peek().val == "]" {
					p.next()
					return arr, nil
				}
				v, err := p.value()
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		case "{":
			obj := map[string]interface{}{}
			for {
				if p.peek().kind == "punct" && p.peek().val == "}" {
					p.next()
					return obj, nil
				}
				kt := p.next()
				if kt.kind != "name" {
					return nil, errGQL("expected object key")
				}
				if _, err := p.expect("punct", ":"); err != nil {
					return nil, err
				}
				v, err := p.value()
				if err != nil {
					return nil, err
				}
				obj[kt.val] = v
			}
		}
	}
	return nil, errGQL("unexpected value token " + t.val)
}

// ---------------------------------------------------------------- runtime objects

type gqlObj struct {
	typ     string
	repo    *repo
	issue   *issue
	pull    *pull
	comment *comment
	// connection payload
	items    []interface{} // *issue | *pull | *fileChange
	total    int
	pageInfo *gqlPageInfo
	// payload objects
	issueObj   *issue
	pullObj    *pull
	commentObj *comment
	// introspection
	typeName string
	// scalar wrappers
	strVal  string
	intVal  int
	boolVal bool
}

type gqlPageInfo struct {
	hasNextPage bool
	endCursor   string
}

func resolveVars(v interface{}, vars map[string]interface{}) interface{} {
	switch x := v.(type) {
	case gqlVar:
		if val, ok := vars[x.name]; ok {
			return val
		}
		return nil
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, e := range x {
			out[i] = resolveVars(e, vars)
		}
		return out
	case map[string]interface{}:
		out := map[string]interface{}{}
		for k, e := range x {
			out[k] = resolveVars(e, vars)
		}
		return out
	}
	return v
}

func argStr(args map[string]interface{}, name string, vars map[string]interface{}) string {
	if args == nil {
		return ""
	}
	return jstr(resolveVars(args[name], vars))
}

func argInt(args map[string]interface{}, name string, vars map[string]interface{}, def int) int {
	if args == nil {
		return def
	}
	v, ok := args[name]
	if !ok {
		return def
	}
	rv := resolveVars(v, vars)
	if f, ok := rv.(float64); ok {
		return int(f)
	}
	return jnum(rv)
}

// ---------------------------------------------------------------- executor

type gqlExec struct {
	st     *store
	doc    *gqlDoc
	vars   map[string]interface{}
	errors []string
}

func (e *gqlExec) renderValue(v interface{}, sel *gqlSelSet) interface{} {
	switch x := v.(type) {
	case *gqlObj:
		return e.renderObj(x, sel)
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, el := range x {
			out[i] = e.renderValue(el, sel)
		}
		return out
	case nil:
		return nil
	default:
		return x
	}
}

func (e *gqlExec) renderObj(obj *gqlObj, sel *gqlSelSet) map[string]interface{} {
	out := map[string]interface{}{}
	if sel == nil {
		return out
	}
	for _, f := range sel.fields {
		if f.fragName != "" {
			frag, ok := e.doc.frags[f.fragName]
			if !ok {
				continue
			}
			sub := e.renderObj(obj, frag)
			for k, v := range sub {
				out[k] = v
			}
			continue
		}
		if f.typeCond != "" {
			if !typeMatches(obj.typ, f.typeCond) {
				continue
			}
			sub := e.renderObj(obj, f.sel)
			for k, v := range sub {
				out[k] = v
			}
			continue
		}
		key := f.name
		if f.alias != "" {
			key = f.alias
		}
		val, err := e.resolveField(obj, f)
		if err != nil {
			e.errors = append(e.errors, err.Error())
			out[key] = nil
			continue
		}
		out[key] = e.renderValue(val, f.sel)
	}
	return out
}

func typeMatches(objType, cond string) bool {
	if objType == cond {
		return true
	}
	// node() returns a concrete type; inline fragments on interfaces match
	// their concrete implementations
	switch cond {
	case "Node":
		switch objType {
		case "Repository", "Issue", "PullRequest", "User":
			return true
		}
	case "RepositoryOwner":
		return objType == "User"
	}
	return false
}

var errUnknownField = func(field, typ string) error {
	return errGQL(`Cannot query field "` + field + `" on type "` + typ + `"`)
}

func (e *gqlExec) resolveField(obj *gqlObj, f *gqlField) (interface{}, error) {
	switch obj.typ {
	case "Query":
		return e.resolveQuery(f)
	case "Mutation":
		return e.resolveMutation(f)
	case "User":
		switch f.name {
		case "login":
			return obj.strVal, nil
		case "id":
			return globalID("User", itoa(viewerID)), nil
		case "name":
			return viewerName, nil
		case "databaseId":
			return viewerID, nil
		}
	case "Repository":
		return e.resolveRepository(obj, f)
	case "RepositoryOwner":
		if f.name == "login" {
			return obj.strVal, nil
		}
		if f.name == "id" {
			return globalID("User", itoa(viewerID)), nil
		}
	case "BranchRef":
		if f.name == "name" {
			return obj.strVal, nil
		}
	case "Issue":
		return e.resolveIssue(obj, f)
	case "PullRequest":
		return e.resolvePullRequest(obj, f)
	case "IssueConnection", "PullRequestConnection", "FilesConnection":
		switch f.name {
		case "nodes":
			var out []interface{}
			for _, it := range obj.items {
				out = append(out, it)
			}
			return out, nil
		case "edges":
			// edges { node { ... } } — model as objects with a node field
			var out []interface{}
			for _, it := range obj.items {
				out = append(out, &gqlObj{typ: "Edge", issueObj: obj.issueObj, pullObj: obj.pullObj, commentObj: obj.commentObj, items: []interface{}{it}})
			}
			return out, nil
		case "totalCount":
			return obj.total, nil
		case "pageInfo":
			return &gqlObj{typ: "PageInfo", pageInfo: obj.pageInfo}, nil
		}
	case "Edge":
		if f.name == "node" && len(obj.items) > 0 {
			return obj.items[0], nil
		}
		if f.name == "cursor" {
			return "", nil
		}
	case "PageInfo":
		switch f.name {
		case "hasNextPage":
			return obj.pageInfo.hasNextPage, nil
		case "endCursor":
			if obj.pageInfo.endCursor == "" {
				return nil, nil
			}
			return obj.pageInfo.endCursor, nil
		case "hasPreviousPage":
			return false, nil
		case "startCursor":
			return nil, nil
		}
	case "File":
		if f.name == "path" {
			return obj.strVal, nil
		}
	case "Comment":
		switch f.name {
		case "id":
			return globalID("Comment", itoa(obj.repo.id), itoa(obj.comment.id)), nil
		case "body":
			return obj.comment.body, nil
		case "databaseId":
			return obj.comment.id, nil
		}
	case "CommentEdge":
		if f.name == "node" {
			return &gqlObj{typ: "Comment", repo: obj.repo, comment: obj.commentObj}, nil
		}
	case "CreateIssuePayload", "CloseIssuePayload":
		if f.name == "issue" {
			return &gqlObj{typ: "Issue", repo: obj.repo, issue: obj.issueObj}, nil
		}
	case "CreatePullRequestPayload", "MergePullRequestPayload":
		if f.name == "pullRequest" {
			return &gqlObj{typ: "PullRequest", repo: obj.repo, pull: obj.pullObj}, nil
		}
	case "AddCommentPayload":
		if f.name == "commentEdge" {
			return &gqlObj{typ: "CommentEdge", repo: obj.repo, commentObj: obj.commentObj}, nil
		}
	case "__Type":
		return e.resolveType(obj, f)
	case "__Field":
		if f.name == "name" {
			return obj.strVal, nil
		}
		if f.name == "type" {
			return &gqlObj{typ: "__Type", typeName: "String"}, nil
		}
	case "__EnumValue":
		if f.name == "name" {
			return obj.strVal, nil
		}
	}
	return nil, errUnknownField(f.name, obj.typ)
}

func (e *gqlExec) resolveQuery(f *gqlField) (interface{}, error) {
	switch f.name {
	case "viewer":
		return &gqlObj{typ: "User", strVal: viewer}, nil
	case "repository":
		owner := argStr(f.args, "owner", e.vars)
		name := argStr(f.args, "name", e.vars)
		e.st.mu.RLock()
		r := e.st.getRepo(owner, name)
		e.st.mu.RUnlock()
		if r == nil {
			return nil, nil
		}
		return &gqlObj{typ: "Repository", repo: r}, nil
	case "node":
		id := argStr(f.args, "id", e.vars)
		return e.resolveNode(id), nil
	case "__type":
		name := argStr(f.args, "name", e.vars)
		if _, ok := introspectedTypes[name]; ok {
			return &gqlObj{typ: "__Type", typeName: name}, nil
		}
		return nil, nil
	case "__schema":
		return &gqlObj{typ: "__Schema"}, nil
	}
	return nil, errUnknownField(f.name, "Query")
}

func (e *gqlExec) resolveNode(id string) interface{} {
	parts := decodeGlobalID(id)
	if len(parts) < 2 {
		return nil
	}
	e.st.mu.RLock()
	defer e.st.mu.RUnlock()
	switch parts[0] {
	case "Repository":
		rid, _ := strconv.Atoi(parts[1])
		for _, r := range e.st.repos {
			if r.id == rid {
				return &gqlObj{typ: "Repository", repo: r}
			}
		}
	case "Issue":
		if len(parts) < 3 {
			return nil
		}
		rid, _ := strconv.Atoi(parts[1])
		num, _ := strconv.Atoi(parts[2])
		for _, r := range e.st.repos {
			if r.id == rid {
				if is, ok := r.issues[num]; ok {
					return &gqlObj{typ: "Issue", repo: r, issue: is}
				}
			}
		}
	case "PullRequest":
		if len(parts) < 3 {
			return nil
		}
		rid, _ := strconv.Atoi(parts[1])
		num, _ := strconv.Atoi(parts[2])
		for _, r := range e.st.repos {
			if r.id == rid {
				if p, ok := r.pulls[num]; ok {
					return &gqlObj{typ: "PullRequest", repo: r, pull: p}
				}
			}
		}
	case "User":
		return &gqlObj{typ: "User", strVal: viewer}
	}
	return nil
}

func (e *gqlExec) resolveRepository(obj *gqlObj, f *gqlField) (interface{}, error) {
	r := obj.repo
	switch f.name {
	case "id":
		return globalID("Repository", itoa(r.id)), nil
	case "databaseId":
		return r.id, nil
	case "name":
		return r.name, nil
	case "nameWithOwner":
		return r.owner + "/" + r.name, nil
	case "owner":
		return &gqlObj{typ: "RepositoryOwner", strVal: r.owner}, nil
	case "defaultBranchRef":
		return &gqlObj{typ: "BranchRef", strVal: r.defaultBranch}, nil
	case "viewerPermission":
		return "ADMIN", nil
	case "hasIssuesEnabled":
		return true, nil
	case "description":
		return r.desc, nil
	case "isPrivate":
		return r.priv, nil
	case "createdAt":
		return "2025-01-01T00:00:00Z", nil
	case "issue":
		num := argInt(f.args, "number", e.vars, 0)
		e.st.mu.RLock()
		is, ok := r.issues[num]
		e.st.mu.RUnlock()
		if !ok {
			return nil, nil
		}
		return &gqlObj{typ: "Issue", repo: r, issue: is}, nil
	case "issues":
		return e.issuesConnection(r, f), nil
	case "pullRequests":
		return e.pullsConnection(r, f), nil
	case "pullRequest":
		num := argInt(f.args, "number", e.vars, 0)
		e.st.mu.RLock()
		p, ok := r.pulls[num]
		e.st.mu.RUnlock()
		if !ok {
			return nil, nil
		}
		return &gqlObj{typ: "PullRequest", repo: r, pull: p}, nil
	}
	return nil, errUnknownField(f.name, "Repository")
}

func (e *gqlExec) issuesConnection(r *repo, f *gqlField) interface{} {
	first := argInt(f.args, "first", e.vars, 30)
	after := argStr(f.args, "after", e.vars)
	var states []string
	if v, ok := f.args["states"]; ok {
		rv := resolveVars(v, e.vars)
		for _, s := range jarr(rv) {
			states = append(states, jstr(s))
		}
		if s, ok := rv.(string); ok && s != "" {
			states = []string{s}
		}
	}
	var orderDesc = true
	if v, ok := f.args["orderBy"]; ok {
		rv := jobj(resolveVars(v, e.vars))
		if d := jstr(rv["direction"]); d == "ASC" {
			orderDesc = false
		}
	}
	e.st.mu.RLock()
	defer e.st.mu.RUnlock()
	var nums []int
	for _, n := range r.issueOrder {
		is := r.issues[n]
		if len(states) > 0 {
			want := ""
			if is.state == "open" {
				want = "OPEN"
			} else {
				want = "CLOSED"
			}
			match := false
			for _, s := range states {
				if s == want {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		nums = append(nums, n)
	}
	if orderDesc {
		for i, j := 0, len(nums)-1; i < j; i, j = i+1, j-1 {
			nums[i], nums[j] = nums[j], nums[i]
		}
	}
	start := 0
	if after != "" {
		if idx, ok := decodeCursor(after); ok {
			start = idx + 1
		}
	}
	if start > len(nums) {
		start = len(nums)
	}
	end := start + first
	if end > len(nums) {
		end = len(nums)
	}
	var items []interface{}
	for _, n := range nums[start:end] {
		items = append(items, &gqlObj{typ: "Issue", repo: r, issue: r.issues[n]})
	}
	pi := &gqlPageInfo{hasNextPage: end < len(nums)}
	if end > start {
		pi.endCursor = encodeCursor(end - 1)
	}
	return &gqlObj{typ: "IssueConnection", repo: r, items: items, total: len(nums), pageInfo: pi}
}

func (e *gqlExec) pullsConnection(r *repo, f *gqlField) interface{} {
	first := argInt(f.args, "first", e.vars, 30)
	after := argStr(f.args, "after", e.vars)
	e.st.mu.RLock()
	defer e.st.mu.RUnlock()
	nums := append([]int(nil), r.pullOrder...)
	start := 0
	if after != "" {
		if idx, ok := decodeCursor(after); ok {
			start = idx + 1
		}
	}
	if start > len(nums) {
		start = len(nums)
	}
	end := start + first
	if end > len(nums) {
		end = len(nums)
	}
	var items []interface{}
	for _, n := range nums[start:end] {
		items = append(items, &gqlObj{typ: "PullRequest", repo: r, pull: r.pulls[n]})
	}
	pi := &gqlPageInfo{hasNextPage: end < len(nums)}
	if end > start {
		pi.endCursor = encodeCursor(end - 1)
	}
	return &gqlObj{typ: "PullRequestConnection", repo: r, items: items, total: len(nums), pageInfo: pi}
}

func encodeCursor(idx int) string {
	return base64.StdEncoding.EncodeToString([]byte("cursor:" + itoa(idx)))
}

func decodeCursor(s string) (int, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	if !strings.HasPrefix(string(b), "cursor:") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(b), "cursor:"))
	if err != nil {
		return 0, false
	}
	return n, true
}

func (e *gqlExec) resolveIssue(obj *gqlObj, f *gqlField) (interface{}, error) {
	is := obj.issue
	switch f.name {
	case "id":
		return globalID("Issue", itoa(obj.repo.id), itoa(is.number)), nil
	case "databaseId":
		return is.id, nil
	case "number":
		return is.number, nil
	case "title":
		return is.title, nil
	case "body":
		return is.body, nil
	case "state":
		if is.state == "open" {
			return "OPEN", nil
		}
		return "CLOSED", nil
	case "createdAt":
		return tsFor(is.seq), nil
	case "url":
		return "https://github.com/" + obj.repo.owner + "/" + obj.repo.name + "/issues/" + itoa(is.number), nil
	case "author":
		return &gqlObj{typ: "User", strVal: viewer}, nil
	case "repository":
		return &gqlObj{typ: "Repository", repo: obj.repo}, nil
	}
	return nil, errUnknownField(f.name, "Issue")
}

func (e *gqlExec) resolvePullRequest(obj *gqlObj, f *gqlField) (interface{}, error) {
	p := obj.pull
	r := obj.repo
	switch f.name {
	case "id":
		return globalID("PullRequest", itoa(r.id), itoa(p.number)), nil
	case "databaseId":
		return p.id, nil
	case "number":
		return p.number, nil
	case "title":
		return p.title, nil
	case "body":
		return p.body, nil
	case "state":
		if p.merged {
			return "MERGED", nil
		}
		if p.state == "open" {
			return "OPEN", nil
		}
		return "CLOSED", nil
	case "merged":
		return p.merged, nil
	case "headRefName":
		return p.headRef, nil
	case "baseRefName":
		return p.baseRef, nil
	case "headRefOid":
		return p.headSha, nil
	case "baseRefOid":
		return p.baseSha, nil
	case "mergeable":
		return e.pullMergeable(r, p), nil
	case "createdAt":
		return tsFor(p.seq), nil
	case "url":
		return "https://github.com/" + r.owner + "/" + r.name + "/pull/" + itoa(p.number), nil
	case "repository":
		return &gqlObj{typ: "Repository", repo: r}, nil
	case "files":
		return e.pullFilesConnection(r, p, f), nil
	}
	return nil, errUnknownField(f.name, "PullRequest")
}

func (e *gqlExec) pullMergeable(r *repo, p *pull) string {
	if p.state != "open" || p.merged {
		return "UNKNOWN"
	}
	e.st.mu.RLock()
	defer e.st.mu.RUnlock()
	baseTip, ok := r.refs["refs/heads/"+p.baseRef]
	if !ok {
		return "UNKNOWN"
	}
	if baseTip == p.baseSha {
		return "MERGEABLE"
	}
	baseFiles := r.filesOfCommit(p.baseSha)
	headFiles := r.filesOfCommit(p.headSha)
	curBaseFiles := r.filesOfCommit(baseTip)
	headChanged := map[string]bool{}
	for _, c := range r.diffFiles(baseFiles, headFiles) {
		headChanged[c.path] = true
	}
	for _, c := range r.diffFiles(baseFiles, curBaseFiles) {
		if headChanged[c.path] {
			return "CONFLICTING"
		}
	}
	return "MERGEABLE"
}

func (e *gqlExec) pullFilesConnection(r *repo, p *pull, f *gqlField) interface{} {
	first := argInt(f.args, "first", e.vars, 30)
	after := argStr(f.args, "after", e.vars)
	e.st.mu.RLock()
	defer e.st.mu.RUnlock()
	baseFiles := r.filesOfCommit(p.baseSha)
	headFiles := r.filesOfCommit(p.headSha)
	changes := r.diffFiles(baseFiles, headFiles)
	start := 0
	if after != "" {
		if idx, ok := decodeCursor(after); ok {
			start = idx + 1
		}
	}
	if start > len(changes) {
		start = len(changes)
	}
	end := start + first
	if end > len(changes) {
		end = len(changes)
	}
	var items []interface{}
	for _, c := range changes[start:end] {
		cc := c
		items = append(items, &gqlObj{typ: "File", strVal: cc.path})
	}
	pi := &gqlPageInfo{hasNextPage: end < len(changes)}
	if end > start {
		pi.endCursor = encodeCursor(end - 1)
	}
	return &gqlObj{typ: "FilesConnection", repo: r, items: items, total: len(changes), pageInfo: pi}
}

// ---------------------------------------------------------------- introspection

var introspectedTypes = map[string]map[string][]string{
	"Issue": {
		"fields": {"id", "databaseId", "number", "title", "body", "state", "createdAt", "updatedAt", "url", "author", "labels", "comments", "repository"},
	},
	"PullRequest": {
		"fields": {"id", "databaseId", "number", "title", "body", "state", "merged", "headRefName", "baseRefName", "mergeable", "files", "headRefOid", "baseRefOid", "createdAt", "updatedAt", "url", "repository"},
	},
	"Repository": {
		"fields": {"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequests", "pullRequest", "description", "isPrivate", "createdAt", "url"},
	},
	"User": {
		"fields": {"id", "login", "name", "email", "databaseId"},
	},
	"SearchType": {
		"enumValues": {"ISSUE", "REPOSITORY", "USER", "DISCUSSION", "CODE", "COMMIT", "MILESTONE", "WIKI"},
	},
}

func (e *gqlExec) resolveType(obj *gqlObj, f *gqlField) (interface{}, error) {
	def := introspectedTypes[obj.typeName]
	switch f.name {
	case "name":
		return obj.typeName, nil
	case "kind":
		if _, ok := def["enumValues"]; ok {
			return "ENUM", nil
		}
		return "OBJECT", nil
	case "fields":
		var out []interface{}
		for _, name := range def["fields"] {
			out = append(out, &gqlObj{typ: "__Field", strVal: name})
		}
		return out, nil
	case "enumValues":
		var out []interface{}
		for _, name := range def["enumValues"] {
			out = append(out, &gqlObj{typ: "__EnumValue", strVal: name})
		}
		return out, nil
	}
	return nil, errUnknownField(f.name, "__Type")
}

// ---------------------------------------------------------------- mutations

func (e *gqlExec) resolveMutation(f *gqlField) (interface{}, error) {
	input := map[string]interface{}{}
	if v, ok := f.args["input"]; ok {
		if m, ok := resolveVars(v, e.vars).(map[string]interface{}); ok {
			input = m
		}
	}
	switch f.name {
	case "createIssue":
		return e.mutCreateIssue(input)
	case "closeIssue":
		return e.mutCloseIssue(input)
	case "createPullRequest":
		return e.mutCreatePullRequest(input)
	case "mergePullRequest":
		return e.mutMergePullRequest(input)
	case "addComment":
		return e.mutAddComment(input)
	}
	return nil, errUnknownField(f.name, "Mutation")
}

func (e *gqlExec) repoByGlobalID(id string) *repo {
	parts := decodeGlobalID(id)
	if len(parts) < 2 || parts[0] != "Repository" {
		return nil
	}
	rid, _ := strconv.Atoi(parts[1])
	for _, r := range e.st.repos {
		if r.id == rid {
			return r
		}
	}
	return nil
}

func (e *gqlExec) mutCreateIssue(input map[string]interface{}) (interface{}, error) {
	title := jstr(input["title"])
	if title == "" {
		return nil, errGQL("title is required")
	}
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	r := e.repoByGlobalID(jstr(input["repositoryId"]))
	if r == nil {
		return nil, errGQL("Repository not found")
	}
	num := r.nextIssue
	r.nextIssue++
	is := &issue{
		id:     e.st.nextIssueID,
		number: num,
		title:  title,
		body:   jstr(input["body"]),
		state:  "open",
		seq:    int(e.st.reqSeq),
	}
	e.st.nextIssueID++
	e.st.reqSeq++
	r.issues[num] = is
	r.issueOrder = append(r.issueOrder, num)
	return &gqlObj{typ: "CreateIssuePayload", repo: r, issueObj: is}, nil
}

func (e *gqlExec) mutCloseIssue(input map[string]interface{}) (interface{}, error) {
	parts := decodeGlobalID(jstr(input["issueId"]))
	if len(parts) < 3 || parts[0] != "Issue" {
		return nil, errGQL("Issue not found")
	}
	rid, _ := strconv.Atoi(parts[1])
	num, _ := strconv.Atoi(parts[2])
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, r := range e.st.repos {
		if r.id != rid {
			continue
		}
		is, ok := r.issues[num]
		if !ok {
			return nil, errGQL("Issue not found")
		}
		is.state = "closed"
		delete(r.issueJSON, num)
		return &gqlObj{typ: "CloseIssuePayload", repo: r, issueObj: is}, nil
	}
	return nil, errGQL("Issue not found")
}

func (e *gqlExec) mutCreatePullRequest(input map[string]interface{}) (interface{}, error) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	r := e.repoByGlobalID(jstr(input["repositoryId"]))
	if r == nil {
		return nil, errGQL("Repository not found")
	}
	head := jstr(input["headRefName"])
	base := jstr(input["baseRefName"])
	title := jstr(input["title"])
	headSha, ok1 := r.refs["refs/heads/"+head]
	baseSha, ok2 := r.refs["refs/heads/"+base]
	if !ok1 || !ok2 {
		return nil, errGQL("head or base branch does not exist")
	}
	for _, n := range r.pullOrder {
		p := r.pulls[n]
		if p.headRef == head && p.state == "open" {
			return nil, errGQL("A pull request already exists for " + head)
		}
	}
	num := r.nextPull
	r.nextPull++
	p := &pull{
		id:      e.st.nextPullID,
		number:  num,
		title:   title,
		body:    jstr(input["body"]),
		state:   "open",
		headRef: head,
		baseRef: base,
		headSha: headSha,
		baseSha: baseSha,
		seq:     int(e.st.reqSeq),
	}
	e.st.nextPullID++
	e.st.reqSeq++
	r.pulls[num] = p
	r.pullOrder = append(r.pullOrder, num)
	return &gqlObj{typ: "CreatePullRequestPayload", repo: r, pullObj: p}, nil
}

func (e *gqlExec) mutMergePullRequest(input map[string]interface{}) (interface{}, error) {
	parts := decodeGlobalID(jstr(input["pullRequestId"]))
	if len(parts) < 3 || parts[0] != "PullRequest" {
		return nil, errGQL("Pull request not found")
	}
	rid, _ := strconv.Atoi(parts[1])
	num, _ := strconv.Atoi(parts[2])
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, r := range e.st.repos {
		if r.id != rid {
			continue
		}
		p, ok := r.pulls[num]
		if !ok {
			return nil, errGQL("Pull request not found")
		}
		if p.merged || p.state != "open" {
			return nil, errGQL("Pull request already merged")
		}
		baseTip, ok := r.refs["refs/heads/"+p.baseRef]
		if !ok {
			return nil, errGQL("Base branch not found")
		}
		if baseTip != p.baseSha {
			baseFiles := r.filesOfCommit(p.baseSha)
			headFiles := r.filesOfCommit(p.headSha)
			curBaseFiles := r.filesOfCommit(baseTip)
			headChanged := map[string]bool{}
			for _, c := range r.diffFiles(baseFiles, headFiles) {
				headChanged[c.path] = true
			}
			for _, c := range r.diffFiles(baseFiles, curBaseFiles) {
				if headChanged[c.path] {
					return nil, errGQL("Merge conflict")
				}
			}
		}
		headCI := r.getCommit(p.headSha)
		if headCI == nil {
			return nil, errGQL("Head commit not found")
		}
		r.seq++
		mergeOID := r.putObject("commit", commitBytes(headCI.tree, []string{baseTip}, p.title, r.authorLine(), baseEpoch+int64(r.seq)*60))
		r.refs["refs/heads/"+p.baseRef] = mergeOID
		r.version++
		p.merged = true
		p.state = "closed"
		p.mergeSha = mergeOID
		p.mergedAt = tsFor(r.seq)
		return &gqlObj{typ: "MergePullRequestPayload", repo: r, pullObj: p}, nil
	}
	return nil, errGQL("Pull request not found")
}

func (e *gqlExec) mutAddComment(input map[string]interface{}) (interface{}, error) {
	parts := decodeGlobalID(jstr(input["subjectId"]))
	if len(parts) < 3 {
		return nil, errGQL("Subject not found")
	}
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	rid, _ := strconv.Atoi(parts[1])
	num, _ := strconv.Atoi(parts[2])
	for _, r := range e.st.repos {
		if r.id != rid {
			continue
		}
		if _, ok := r.issues[num]; !ok {
			return nil, errGQL("Issue not found")
		}
		c := &comment{id: e.st.nextCommentID, body: jstr(input["body"])}
		e.st.nextCommentID++
		r.comments[num] = append(r.comments[num], c)
		return &gqlObj{typ: "AddCommentPayload", repo: r, commentObj: c}, nil
	}
	return nil, errGQL("Subject not found")
}

// ---------------------------------------------------------------- HTTP handler

func handleGraphQL(st *store, req *httpReq) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	var requests []map[string]interface{}
	switch v := body.(type) {
	case map[string]interface{}:
		requests = []map[string]interface{}{v}
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				requests = append(requests, m)
			}
		}
	default:
		return jsonError(400, "Invalid GraphQL request")
	}
	results := make([]interface{}, 0, len(requests))
	for _, rq := range requests {
		results = append(results, executeOneGraphQL(st, rq))
	}
	if len(requests) == 1 {
		return jsonResp(200, marshalJSON(results[0]))
	}
	return jsonResp(200, marshalJSON(results))
}

func executeOneGraphQL(st *store, rq map[string]interface{}) interface{} {
	query := jstr(rq["query"])
	vars := map[string]interface{}{}
	if v, ok := rq["variables"]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			vars = m
		}
	}
	doc, err := parseGQL(query)
	if err != nil {
		return map[string]interface{}{"errors": []interface{}{map[string]interface{}{"message": err.Error()}}}
	}
	op := doc.ops[0]
	if name, ok := rq["operationName"]; ok {
		if n := jstr(name); n != "" {
			for _, o := range doc.ops {
				if o.name == n {
					op = o
				}
			}
		}
	}
	// apply variable defaults
	for name, def := range op.varDefs {
		if _, ok := vars[name]; !ok && def.hasDef {
			vars[name] = def.defVal
		}
	}
	exec := &gqlExec{st: st, doc: doc, vars: vars}
	rootType := "Query"
	if op.opType == "mutation" {
		rootType = "Mutation"
	}
	data := exec.renderObj(&gqlObj{typ: rootType}, op.sel)
	out := map[string]interface{}{"data": data}
	if len(exec.errors) > 0 {
		var errs []interface{}
		for _, msg := range exec.errors {
			errs = append(errs, map[string]interface{}{"message": msg})
		}
		out["errors"] = errs
	}
	return out
}

// ---------------------------------------------------------------- JSON marshaling of results

func marshalJSON(v interface{}) []byte {
	var b strings.Builder
	writeJSONValue(&b, v)
	return []byte(b.String())
}

func writeJSONValue(b *strings.Builder, v interface{}) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(itoa(x))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
	case string:
		b.WriteString(jsonString(x))
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(jsonString(k))
			b.WriteString(":")
			writeJSONValue(b, x[k])
		}
		b.WriteString("}")
	case []interface{}:
		b.WriteString("[")
		for i, el := range x {
			if i > 0 {
				b.WriteString(",")
			}
			writeJSONValue(b, el)
		}
		b.WriteString("]")
	default:
		b.WriteString("null")
	}
}
