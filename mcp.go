package main

import (
	"strings"
)

const mcpProtocolVersion = "2026-07-28"

var mcpTools = []map[string]interface{}{
	{
		"name":        "create_repo",
		"description": "Create a new repository owned by the authenticated user.",
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}, "description": map[string]interface{}{"type": "string"}, "private": map[string]interface{}{"type": "boolean"}},
			"required":   []interface{}{"name"},
		},
	},
	{
		"name":        "get_repo",
		"description": "Get a repository by owner and name.",
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"owner": map[string]interface{}{"type": "string"}, "repo": map[string]interface{}{"type": "string"}},
			"required":   []interface{}{"owner", "repo"},
		},
	},
	{
		"name":        "create_issue",
		"description": "Create an issue in a repository.",
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"owner": map[string]interface{}{"type": "string"}, "repo": map[string]interface{}{"type": "string"}, "title": map[string]interface{}{"type": "string"}, "body": map[string]interface{}{"type": "string"}},
			"required":   []interface{}{"owner", "repo", "title"},
		},
	},
	{
		"name":        "list_issues",
		"description": "List issues of a repository.",
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"owner": map[string]interface{}{"type": "string"}, "repo": map[string]interface{}{"type": "string"}, "state": map[string]interface{}{"type": "string"}},
			"required":   []interface{}{"owner", "repo"},
		},
	},
	{
		"name":        "create_pull_request",
		"description": "Create a pull request in a repository.",
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"owner": map[string]interface{}{"type": "string"}, "repo": map[string]interface{}{"type": "string"}, "title": map[string]interface{}{"type": "string"}, "body": map[string]interface{}{"type": "string"}, "head": map[string]interface{}{"type": "string"}, "base": map[string]interface{}{"type": "string"}},
			"required":   []interface{}{"owner", "repo", "title", "head", "base"},
		},
	},
	{
		"name":        "merge_pull_request",
		"description": "Merge a pull request with the squash method.",
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"owner": map[string]interface{}{"type": "string"}, "repo": map[string]interface{}{"type": "string"}, "pull_number": map[string]interface{}{"type": "number"}},
			"required":   []interface{}{"owner", "repo", "pull_number"},
		},
	},
}

func mcpError(id interface{}, code int, message string) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]interface{}{"code": code, "message": message},
	}
}

func mcpResult(id interface{}, result map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpToolResult(id interface{}, structured map[string]interface{}, text string) map[string]interface{} {
	return mcpResult(id, map[string]interface{}{
		"content":           []interface{}{map[string]interface{}{"type": "text", "text": text}},
		"structuredContent": structured,
	})
}

func mcpToolError(id interface{}, message string) map[string]interface{} {
	return mcpResult(id, map[string]interface{}{
		"content":           []interface{}{map[string]interface{}{"type": "text", "text": message}},
		"structuredContent": map[string]interface{}{"error": message},
		"isError":           true,
	})
}

func handleMCP(st *store, req *httpReq) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		// -32700 parse error, id null
		return jsonResp(400, marshalJSON(mcpError(nil, -32700, "Parse error")))
	}
	m, ok := body.(map[string]interface{})
	if !ok {
		return jsonResp(400, marshalJSON(mcpError(nil, -32700, "Parse error")))
	}
	id := m["id"]
	method := jstr(m["method"])
	params := jobj(m["params"])
	switch method {
	case "server/discover":
		return jsonResp(200, marshalJSON(mcpResult(id, map[string]interface{}{
			"supportedVersions": []interface{}{mcpProtocolVersion, "2025-11-25", "2025-06-18"},
			"capabilities":      map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":        map[string]interface{}{"name": "gitforge", "version": "1.0.0"},
		})))
	case "initialize":
		return jsonResp(200, marshalJSON(mcpResult(id, map[string]interface{}{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "gitforge", "version": "1.0.0"},
		})))
	case "notifications/initialized":
		return &httpResp{status: 202, contentType: "application/json", body: []byte(`{}`)}
	case "tools/list":
		var tools []interface{}
		for _, t := range mcpTools {
			tools = append(tools, t)
		}
		return jsonResp(200, marshalJSON(mcpResult(id, map[string]interface{}{"tools": tools})))
	case "tools/call":
		return jsonResp(200, marshalJSON(mcpCallTool(st, id, params)))
	default:
		return jsonResp(200, marshalJSON(mcpError(id, -32601, "Method not found: "+method)))
	}
}

func mcpCallTool(st *store, id interface{}, params map[string]interface{}) map[string]interface{} {
	name := jstr(params["name"])
	args := jobj(params["arguments"])
	switch name {
	case "create_repo":
		return mcpCreateRepo(st, id, args)
	case "get_repo":
		return mcpGetRepo(st, id, args)
	case "create_issue":
		return mcpCreateIssue(st, id, args)
	case "list_issues":
		return mcpListIssues(st, id, args)
	case "create_pull_request":
		return mcpCreatePullRequest(st, id, args)
	case "merge_pull_request":
		return mcpMergePullRequest(st, id, args)
	default:
		return mcpError(id, -32601, "Unknown tool: "+name)
	}
}

func mcpMissingArg(id interface{}, name string) map[string]interface{} {
	return mcpToolError(id, "Missing required argument: "+name)
}

func mcpCreateRepo(st *store, id interface{}, args map[string]interface{}) map[string]interface{} {
	name := jstr(args["name"])
	if name == "" {
		return mcpMissingArg(id, "name")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r, ok := st.createRepo(viewer, name, jstr(args["description"]), jbool(args["private"]))
	if !ok {
		return mcpToolError(id, "Repository already exists")
	}
	structured := map[string]interface{}{
		"id": r.id, "name": r.name, "full_name": r.owner + "/" + r.name,
		"owner": map[string]interface{}{"login": r.owner},
		"description": r.desc, "private": r.priv, "default_branch": r.defaultBranch,
	}
	return mcpToolResult(id, structured, string(marshalJSON(structured)))
}

func mcpGetRepo(st *store, id interface{}, args map[string]interface{}) map[string]interface{} {
	owner := jstr(args["owner"])
	repoName := jstr(args["repo"])
	if owner == "" {
		return mcpMissingArg(id, "owner")
	}
	if repoName == "" {
		return mcpMissingArg(id, "repo")
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, repoName)
	if r == nil {
		return mcpToolError(id, "Repository not found")
	}
	structured := map[string]interface{}{
		"id": r.id, "name": r.name, "full_name": r.owner + "/" + r.name,
		"owner": map[string]interface{}{"login": r.owner},
		"description": r.desc, "private": r.priv, "default_branch": r.defaultBranch,
	}
	return mcpToolResult(id, structured, string(marshalJSON(structured)))
}

func mcpCreateIssue(st *store, id interface{}, args map[string]interface{}) map[string]interface{} {
	owner := jstr(args["owner"])
	repoName := jstr(args["repo"])
	title := jstr(args["title"])
	if owner == "" {
		return mcpMissingArg(id, "owner")
	}
	if repoName == "" {
		return mcpMissingArg(id, "repo")
	}
	if title == "" {
		return mcpMissingArg(id, "title")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, repoName)
	if r == nil {
		return mcpToolError(id, "Repository not found")
	}
	num := r.nextIssue
	r.nextIssue++
	is := &issue{
		id:     st.nextIssueID,
		number: num,
		title:  title,
		body:   jstr(args["body"]),
		state:  "open",
		seq:    int(st.reqSeq),
	}
	st.nextIssueID++
	st.reqSeq++
	r.issues[num] = is
	r.issueOrder = append(r.issueOrder, num)
	structured := map[string]interface{}{
		"id": is.id, "number": is.number, "title": is.title, "body": is.body,
		"state": is.state, "labels": []interface{}{},
	}
	return mcpToolResult(id, structured, string(marshalJSON(structured)))
}

func mcpListIssues(st *store, id interface{}, args map[string]interface{}) map[string]interface{} {
	owner := jstr(args["owner"])
	repoName := jstr(args["repo"])
	if owner == "" {
		return mcpMissingArg(id, "owner")
	}
	if repoName == "" {
		return mcpMissingArg(id, "repo")
	}
	state := jstr(args["state"])
	if state == "" {
		state = "open"
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, repoName)
	if r == nil {
		return mcpToolError(id, "Repository not found")
	}
	var list []interface{}
	for _, n := range r.issueOrder {
		is := r.issues[n]
		if state != "all" && is.state != state {
			continue
		}
		list = append(list, map[string]interface{}{
			"id": is.id, "number": is.number, "title": is.title, "body": is.body,
			"state": is.state,
		})
	}
	structured := map[string]interface{}{"issues": list}
	return mcpToolResult(id, structured, string(marshalJSON(structured)))
}

func mcpCreatePullRequest(st *store, id interface{}, args map[string]interface{}) map[string]interface{} {
	owner := jstr(args["owner"])
	repoName := jstr(args["repo"])
	title := jstr(args["title"])
	head := jstr(args["head"])
	base := jstr(args["base"])
	for _, kv := range [][2]string{{"owner", owner}, {"repo", repoName}, {"title", title}, {"head", head}, {"base", base}} {
		if kv[1] == "" {
			return mcpMissingArg(id, kv[0])
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, repoName)
	if r == nil {
		return mcpToolError(id, "Repository not found")
	}
	headSha, ok1 := r.refs["refs/heads/"+head]
	baseSha, ok2 := r.refs["refs/heads/"+base]
	if !ok1 || !ok2 {
		return mcpToolError(id, "head or base branch does not exist")
	}
	for _, n := range r.pullOrder {
		p := r.pulls[n]
		if p.headRef == head && p.state == "open" {
			return mcpToolError(id, "A pull request already exists for "+head)
		}
	}
	num := r.nextPull
	r.nextPull++
	p := &pull{
		id:      st.nextPullID,
		number:  num,
		title:   title,
		body:    jstr(args["body"]),
		state:   "open",
		headRef: head,
		baseRef: base,
		headSha: headSha,
		baseSha: baseSha,
		seq:     int(st.reqSeq),
	}
	st.nextPullID++
	st.reqSeq++
	r.pulls[num] = p
	r.pullOrder = append(r.pullOrder, num)
	structured := map[string]interface{}{
		"id": p.id, "number": p.number, "title": p.title, "body": p.body,
		"state": p.state, "head": map[string]interface{}{"ref": p.headRef, "sha": p.headSha},
		"base": map[string]interface{}{"ref": p.baseRef, "sha": p.baseSha},
	}
	return mcpToolResult(id, structured, string(marshalJSON(structured)))
}

func mcpMergePullRequest(st *store, id interface{}, args map[string]interface{}) map[string]interface{} {
	owner := jstr(args["owner"])
	repoName := jstr(args["repo"])
	if owner == "" {
		return mcpMissingArg(id, "owner")
	}
	if repoName == "" {
		return mcpMissingArg(id, "repo")
	}
	pullNumber := jnum(args["pull_number"])
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, repoName)
	if r == nil {
		return mcpToolError(id, "Repository not found")
	}
	p, ok := r.pulls[pullNumber]
	if !ok {
		return mcpToolError(id, "Pull request not found")
	}
	if p.merged || p.state != "open" {
		return mcpToolError(id, "Pull request already merged")
	}
	baseTip, ok := r.refs["refs/heads/"+p.baseRef]
	if !ok {
		return mcpToolError(id, "Base branch not found")
	}
	if baseTip != p.baseSha {
		baseFiles := r.filesOfCommit(p.baseSha)
		headFiles := r.filesOfCommit(p.headSha)
		curBaseFiles := r.filesOfCommit(baseTip)
		headChanged := map[string]bool{}
		for _, c := range r.diffFiles(baseFiles, headFiles) {
			headChanged[c.path] = true
		}
		conflict := false
		for _, c := range r.diffFiles(baseFiles, curBaseFiles) {
			if headChanged[c.path] {
				conflict = true
			}
		}
		if conflict {
			return mcpToolError(id, "Merge conflict")
		}
	}
	headCI := r.getCommit(p.headSha)
	if headCI == nil {
		return mcpToolError(id, "Head commit not found")
	}
	r.seq++
	mergeOID := r.putObject("commit", commitBytes(headCI.tree, []string{baseTip}, p.title, r.authorLine(), baseEpoch+int64(r.seq)*60))
	r.refs["refs/heads/"+p.baseRef] = mergeOID
	r.version++
	p.merged = true
	p.state = "closed"
	p.mergeSha = mergeOID
	p.mergedAt = tsFor(r.seq)
	structured := map[string]interface{}{
		"sha": mergeOID, "merged": true, "message": p.title,
		"number": p.number, "state": "closed",
	}
	return mcpToolResult(id, structured, string(marshalJSON(structured)))
}

var _ = strings.TrimSpace
