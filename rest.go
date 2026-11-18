package main

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

const viewerID = 1000

// ---------------------------------------------------------------- JSON builders

func userJSON(login string, id int) string {
	return `{"login":` + jsonString(login) + `,"id":` + itoa(id) + `,"type":"User"}`
}

func repoJSON(r *repo) string {
	var b strings.Builder
	b.WriteString(`{"id":`)
	b.WriteString(itoa(r.id))
	b.WriteString(`,"name":`)
	b.WriteString(jsonString(r.name))
	b.WriteString(`,"full_name":`)
	b.WriteString(jsonString(r.owner + "/" + r.name))
	b.WriteString(`,"private":`)
	if r.priv {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString(`,"owner":`)
	b.WriteString(userJSON(r.owner, viewerID))
	b.WriteString(`,"description":`)
	b.WriteString(jsonString(r.desc))
	b.WriteString(`,"default_branch":`)
	b.WriteString(jsonString(r.defaultBranch))
	b.WriteString(`,"created_at":"2025-01-01T00:00:00Z","updated_at":`)
	b.WriteString(jsonString(tsFor(r.seq)))
	b.WriteString(`,"html_url":`)
	b.WriteString(jsonString("https://github.com/" + r.owner + "/" + r.name))
	b.WriteString(`,"clone_url":`)
	b.WriteString(jsonString("https://github.com/" + r.owner + "/" + r.name + ".git"))
	b.WriteString(`}`)
	return b.String()
}

func issueJSON(r *repo, is *issue) []byte {
	if cached, ok := r.issueJSON[is.number]; ok {
		return cached
	}
	var b strings.Builder
	b.WriteString(`{"id":`)
	b.WriteString(itoa(is.id))
	b.WriteString(`,"number":`)
	b.WriteString(itoa(is.number))
	b.WriteString(`,"title":`)
	b.WriteString(jsonString(is.title))
	b.WriteString(`,"body":`)
	b.WriteString(jsonString(is.body))
	b.WriteString(`,"state":`)
	b.WriteString(jsonString(is.state))
	b.WriteString(`,"labels":[`)
	for i, name := range is.labels {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":`)
		b.WriteString(jsonString(name))
		b.WriteString(`}`)
	}
	b.WriteString(`],"user":`)
	b.WriteString(userJSON(viewer, viewerID))
	b.WriteString(`,"created_at":`)
	b.WriteString(jsonString(tsFor(is.seq)))
	b.WriteString(`,"updated_at":`)
	b.WriteString(jsonString(tsFor(is.seq)))
	b.WriteString(`,"html_url":`)
	b.WriteString(jsonString("https://github.com/" + r.owner + "/" + r.name + "/issues/" + itoa(is.number)))
	b.WriteString(`}`)
	out := []byte(b.String())
	r.issueJSON[is.number] = out
	return out
}

func pullJSON(r *repo, p *pull) []byte {
	var b strings.Builder
	b.WriteString(`{"id":`)
	b.WriteString(itoa(p.id))
	b.WriteString(`,"number":`)
	b.WriteString(itoa(p.number))
	b.WriteString(`,"title":`)
	b.WriteString(jsonString(p.title))
	b.WriteString(`,"body":`)
	b.WriteString(jsonString(p.body))
	b.WriteString(`,"state":`)
	state := p.state
	if p.merged {
		state = "closed"
	}
	b.WriteString(jsonString(state))
	b.WriteString(`,"head":{"ref":`)
	b.WriteString(jsonString(p.headRef))
	b.WriteString(`,"sha":`)
	b.WriteString(jsonString(p.headSha))
	b.WriteString(`},"base":{"ref":`)
	b.WriteString(jsonString(p.baseRef))
	b.WriteString(`,"sha":`)
	b.WriteString(jsonString(p.baseSha))
	b.WriteString(`},"merged":`)
	if p.merged {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString(`,"merged_at":`)
	if p.mergedAt != "" {
		b.WriteString(jsonString(p.mergedAt))
	} else {
		b.WriteString("null")
	}
	b.WriteString(`,"merge_commit_sha":`)
	if p.mergeSha != "" {
		b.WriteString(jsonString(p.mergeSha))
	} else {
		b.WriteString("null")
	}
	b.WriteString(`,"created_at":`)
	b.WriteString(jsonString(tsFor(p.seq)))
	b.WriteString(`,"html_url":`)
	b.WriteString(jsonString("https://github.com/" + r.owner + "/" + r.name + "/pull/" + itoa(p.number)))
	b.WriteString(`}`)
	return []byte(b.String())
}

func commentJSON(c *comment) string {
	return `{"id":` + itoa(c.id) + `,"body":` + jsonString(c.body) +
		`,"user":` + userJSON(viewer, viewerID) + `,"created_at":"2025-01-01T00:00:00Z"}`
}

// ---------------------------------------------------------------- routing

func handleREST(st *store, req *httpReq) *httpResp {
	segs := splitPath(strings.TrimPrefix(req.path, "/api/v3"))
	if len(segs) == 0 {
		// GET /api/v3/
		if req.method != "GET" {
			return jsonError(404, "Not Found")
		}
		return &httpResp{status: 200, contentType: "application/json",
			headers: [][2]string{{"X-OAuth-Scopes", "repo, read:org"}},
			body:    []byte(`{"current_user_url":"/api/v3/user","repository_url":"/api/v3/repos/{owner}/{repo}"}`)}
	}
	if len(segs) == 1 && segs[0] == "meta" && req.method == "GET" {
		return jsonResp(200, []byte(`{"installed_version":"1.0.0","verifiable_password_authentication":true}`))
	}
	if len(segs) == 1 && segs[0] == "user" && req.method == "GET" {
		return jsonResp(200, []byte(`{"login":`+jsonString(viewer)+`,"id":`+itoa(viewerID)+`,"name":`+jsonString(viewerName)+`,"email":`+jsonString(viewerEmail)+`,"type":"User"}`))
	}
	if len(segs) == 2 && segs[0] == "user" && segs[1] == "repos" {
		if req.method == "GET" {
			return listViewerRepos(st, req)
		}
		if req.method == "POST" {
			return createRepoREST(st, req)
		}
		return jsonError(404, "Not Found")
	}
	if len(segs) >= 3 && segs[0] == "repos" {
		return handleRepoREST(st, req, segs[1:])
	}
	return jsonError(404, "Not Found")
}

func listViewerRepos(st *store, req *httpReq) *httpResp {
	perPage := queryInt(req, "per_page", 30)
	if perPage < 1 {
		perPage = 30
	}
	page := queryInt(req, "page", 1)
	if page < 1 {
		page = 1
	}
	st.mu.RLock()
	repos := st.viewerRepos()
	st.mu.RUnlock()
	total := len(repos)
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	var b strings.Builder
	b.WriteString("[")
	for i := start; i < end; i++ {
		if i > start {
			b.WriteString(",")
		}
		b.WriteString(repoJSON(repos[i]))
	}
	b.WriteString("]")
	resp := jsonResp(200, []byte(b.String()))
	if end < total {
		host := req.header("Host")
		base := "http://" + host + "/api/v3/user/repos?per_page=" + itoa(perPage) + "&page="
		totalPages := (total + perPage - 1) / perPage
		link := "<" + base + itoa(page+1) + `>; rel="next", <` + base + itoa(totalPages) + `>; rel="last"`
		resp.headers = append(resp.headers, [2]string{"Link", link})
	}
	return resp
}

func queryInt(req *httpReq, name string, def int) int {
	v := req.queryGet(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func createRepoREST(st *store, req *httpReq) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	name := jstr(m["name"])
	if name == "" {
		return jsonError(422, "name is required")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r, ok := st.createRepo(viewer, name, jstr(m["description"]), jbool(m["private"]))
	if !ok {
		return jsonError(422, "Repository already exists")
	}
	return jsonResp(201, []byte(repoJSON(r)))
}

// ---------------------------------------------------------------- /repos/{owner}/{repo}...

func handleRepoREST(st *store, req *httpReq, segs []string) *httpResp {
	owner, name := segs[0], segs[1]
	rest := segs[2:]
	if len(rest) == 0 {
		switch req.method {
		case "GET":
			st.mu.RLock()
			r := st.getRepo(owner, name)
			st.mu.RUnlock()
			if r == nil {
				return jsonError(404, "Not Found")
			}
			return jsonResp(200, []byte(repoJSON(r)))
		case "PATCH":
			st.mu.Lock()
			defer st.mu.Unlock()
			r := st.getRepo(owner, name)
			if r == nil {
				return jsonError(404, "Not Found")
			}
			body, err := parseJSON(req.body)
			if err != nil {
				return jsonError(400, "Invalid JSON")
			}
			if d, ok := jobj(body)["description"]; ok {
				r.desc = jstr(d)
			}
			return jsonResp(200, []byte(repoJSON(r)))
		case "DELETE":
			st.mu.Lock()
			defer st.mu.Unlock()
			if !st.deleteRepo(owner, name) {
				return jsonError(404, "Not Found")
			}
			return &httpResp{status: 204, contentType: "application/json", body: nil}
		}
		return jsonError(404, "Not Found")
	}
	switch rest[0] {
	case "git":
		if len(rest) == 2 && rest[1] == "refs" && req.method == "POST" {
			return createRef(st, req, owner, name)
		}
		if len(rest) >= 4 && rest[1] == "refs" && rest[2] == "heads" && req.method == "GET" {
			return getRef(st, req, owner, name, "refs/heads/"+strings.Join(rest[3:], "/"))
		}
		return jsonError(404, "Not Found")
	case "branches":
		if len(rest) == 1 && req.method == "GET" {
			return listBranches(st, owner, name)
		}
		if len(rest) == 2 && req.method == "GET" {
			return getBranch(st, owner, name, rest[1])
		}
		return jsonError(404, "Not Found")
	case "commits":
		if len(rest) == 1 && req.method == "GET" {
			return listCommits(st, req, owner, name)
		}
		return jsonError(404, "Not Found")
	case "contents":
		return handleContents(st, req, owner, name, strings.Join(rest[1:], "/"))
	case "labels":
		if len(rest) == 1 && req.method == "POST" {
			return createLabel(st, req, owner, name)
		}
		return jsonError(404, "Not Found")
	case "issues":
		return handleIssues(st, req, owner, name, rest[1:])
	case "pulls":
		return handlePulls(st, req, owner, name, rest[1:])
	}
	return jsonError(404, "Not Found")
}

func createRef(st *store, req *httpReq, owner, name string) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	ref := jstr(m["ref"])
	sha := jstr(m["sha"])
	if ref == "" || sha == "" {
		return jsonError(422, "ref and sha are required")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if _, exists := r.refs[ref]; exists {
		return jsonError(422, "Reference already exists")
	}
	if r.objects[sha] == nil {
		return jsonError(422, "Object does not exist")
	}
	r.refs[ref] = sha
	r.version++
	return jsonResp(201, []byte(`{"ref":`+jsonString(ref)+`,"object":{"sha":`+jsonString(sha)+`,"type":"commit"}}`))
}

func getRef(st *store, req *httpReq, owner, name, ref string) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	sha, ok := r.refs[ref]
	if !ok {
		return jsonError(404, "Not Found")
	}
	return jsonResp(200, []byte(`{"ref":`+jsonString(ref)+`,"object":{"sha":`+jsonString(sha)+`,"type":"commit"}}`))
}

func listBranches(st *store, owner, name string) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	var names []string
	for ref := range r.refs {
		if strings.HasPrefix(ref, "refs/heads/") {
			names = append(names, strings.TrimPrefix(ref, "refs/heads/"))
		}
	}
	sort.Strings(names)
	// default branch first
	for i, n := range names {
		if n == r.defaultBranch && i != 0 {
			names = append(names[:i], names[i+1:]...)
			names = append([]string{n}, names...)
			break
		}
	}
	var b strings.Builder
	b.WriteString("[")
	for i, n := range names {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":`)
		b.WriteString(jsonString(n))
		b.WriteString(`,"commit":{"sha":`)
		b.WriteString(jsonString(r.refs["refs/heads/"+n]))
		b.WriteString(`}}`)
	}
	b.WriteString("]")
	return jsonResp(200, []byte(b.String()))
}

func getBranch(st *store, owner, name, branch string) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	sha, ok := r.refs["refs/heads/"+branch]
	if !ok {
		return jsonError(404, "Not Found")
	}
	return jsonResp(200, []byte(`{"name":`+jsonString(branch)+`,"commit":{"sha":`+jsonString(sha)+`}}`))
}

func listCommits(st *store, req *httpReq, owner, name string) *httpResp {
	branch := req.queryGet("sha")
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if branch == "" {
		branch = r.defaultBranch
	}
	tip, ok := r.resolveRef(branch)
	if !ok {
		return jsonError(404, "Not Found")
	}
	var b strings.Builder
	b.WriteString("[")
	for i, oid := range r.history(tip) {
		if i > 0 {
			b.WriteString(",")
		}
		ci := r.getCommit(oid)
		msg := ""
		if ci != nil {
			msg = strings.TrimSuffix(ci.message, "\n")
		}
		b.WriteString(`{"sha":`)
		b.WriteString(jsonString(oid))
		b.WriteString(`,"commit":{"message":`)
		b.WriteString(jsonString(msg))
		b.WriteString(`}}`)
	}
	b.WriteString("]")
	return jsonResp(200, []byte(b.String()))
}

// ---------------------------------------------------------------- contents

func handleContents(st *store, req *httpReq, owner, name, path string) *httpResp {
	switch req.method {
	case "GET":
		return getContents(st, req, owner, name, path)
	case "PUT":
		return putContents(st, req, owner, name, path)
	case "DELETE":
		return deleteContents(st, req, owner, name, path)
	}
	return jsonError(404, "Not Found")
}

func getContents(st *store, req *httpReq, owner, name, path string) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	ref := req.queryGet("ref")
	if ref == "" {
		ref = r.defaultBranch
	}
	tip, ok := r.resolveRef(ref)
	if !ok {
		return jsonError(404, "Not Found")
	}
	ci := r.getCommit(tip)
	if ci == nil {
		return jsonError(404, "Not Found")
	}
	if path == "" {
		return jsonResp(200, []byte(dirListingJSON(r, ci.tree, "")))
	}
	// walk the tree to the path
	parts := strings.Split(path, "/")
	treeOID := ci.tree
	for i, part := range parts {
		found := ""
		isDir := false
		for _, e := range r.getTree(treeOID) {
			if e.name == part {
				found = e.oid
				isDir = e.mode == "40000"
				break
			}
		}
		if found == "" {
			return jsonError(404, "Not Found")
		}
		if i == len(parts)-1 {
			if isDir {
				return jsonResp(200, []byte(dirListingJSON(r, found, path)))
			}
			obj := r.objects[found]
			if obj == nil {
				return jsonError(404, "Not Found")
			}
			return jsonResp(200, []byte(fileJSON(path, found, obj.data)))
		}
		if !isDir {
			return jsonError(404, "Not Found")
		}
		treeOID = found
	}
	return jsonError(404, "Not Found")
}

func dirListingJSON(r *repo, treeOID, prefix string) string {
	entries := r.getTree(treeOID)
	var b strings.Builder
	b.WriteString("[")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		typ := "file"
		if e.mode == "40000" {
			typ = "dir"
		}
		p := e.name
		if prefix != "" {
			p = prefix + "/" + e.name
		}
		b.WriteString(`{"type":`)
		b.WriteString(jsonString(typ))
		b.WriteString(`,"name":`)
		b.WriteString(jsonString(e.name))
		b.WriteString(`,"path":`)
		b.WriteString(jsonString(p))
		b.WriteString(`,"sha":`)
		b.WriteString(jsonString(e.oid))
		b.WriteString(`}`)
	}
	b.WriteString("]")
	return b.String()
}

func fileJSON(path, blobOID string, data []byte) string {
	name := path
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		name = path[idx+1:]
	}
	return `{"type":"file","encoding":"base64","content":` +
		jsonString(base64.StdEncoding.EncodeToString(data)) +
		`,"sha":` + jsonString(blobOID) +
		`,"size":` + itoa(len(data)) +
		`,"name":` + jsonString(name) +
		`,"path":` + jsonString(path) + `}`
}

func putContents(st *store, req *httpReq, owner, name, path string) *httpResp {
	if path == "" {
		return jsonError(422, "path is required")
	}
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	content, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(jstr(m["content"])), ""))
	if err != nil {
		return jsonError(400, "Invalid base64 content")
	}
	message := jstr(m["message"])
	if message == "" {
		message = "Update " + path
	}
	branch := jstr(m["branch"])
	sha := jstr(m["sha"])
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if branch == "" {
		branch = r.defaultBranch
	}
	// current state of the file on the branch; a branch that does not exist
	// yet forks the default branch tip, so the file state comes from there
	var curBlob string
	var hasCur bool
	tip, hasTip := r.refs["refs/heads/"+branch]
	if !hasTip {
		tip, hasTip = r.refs["refs/heads/"+r.defaultBranch]
	}
	if hasTip {
		files := r.filesOfCommit(tip)
		curBlob, hasCur = files[path]
	}
	if sha != "" {
		if !hasCur || curBlob != sha {
			return jsonError(409, "sha does not match")
		}
	} else if hasCur {
		return jsonError(422, "file already exists")
	}
	commitOID, blobOID := r.commitFile(branch, path, content, message)
	status := 201
	if sha != "" {
		status = 200
	}
	return jsonResp(status, []byte(`{"content":`+fileJSON(path, blobOID, content)+`,"commit":{"sha":`+jsonString(commitOID)+`}}`))
}

func deleteContents(st *store, req *httpReq, owner, name, path string) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	sha := jstr(m["sha"])
	message := jstr(m["message"])
	if message == "" {
		message = "Delete " + path
	}
	branch := jstr(m["branch"])
	if branch == "" {
		branch = "main"
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	tip, ok := r.refs["refs/heads/"+branch]
	if !ok {
		return jsonError(404, "Not Found")
	}
	files := r.filesOfCommit(tip)
	curBlob, hasCur := files[path]
	if !hasCur {
		return jsonError(404, "Not Found")
	}
	if sha != "" && curBlob != sha {
		return jsonError(409, "sha does not match")
	}
	commitOID, ok := r.deleteFile(branch, path, message)
	if !ok {
		return jsonError(404, "Not Found")
	}
	return jsonResp(200, []byte(`{"commit":{"sha":`+jsonString(commitOID)+`}}`))
}

// ---------------------------------------------------------------- labels

func createLabel(st *store, req *httpReq, owner, name string) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	labelName := jstr(m["name"])
	color := jstr(m["color"])
	if labelName == "" {
		return jsonError(422, "name is required")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if _, exists := r.labels[labelName]; exists {
		return jsonError(422, "Label already exists")
	}
	r.labels[labelName] = &label{name: labelName, color: color}
	return jsonResp(201, []byte(`{"name":`+jsonString(labelName)+`,"color":`+jsonString(color)+`}`))
}

// ---------------------------------------------------------------- issues

func handleIssues(st *store, req *httpReq, owner, name string, rest []string) *httpResp {
	if len(rest) == 0 {
		if req.method == "GET" {
			return listIssues(st, req, owner, name)
		}
		if req.method == "POST" {
			return createIssueREST(st, req, owner, name)
		}
		return jsonError(404, "Not Found")
	}
	num, err := strconv.Atoi(rest[0])
	if err != nil {
		return jsonError(404, "Not Found")
	}
	if len(rest) == 1 {
		if req.method == "GET" {
			return getIssueREST(st, owner, name, num)
		}
		if req.method == "PATCH" {
			return patchIssueREST(st, req, owner, name, num)
		}
		return jsonError(404, "Not Found")
	}
	if len(rest) == 2 && rest[1] == "comments" {
		if req.method == "GET" {
			return listComments(st, owner, name, num)
		}
		if req.method == "POST" {
			return createCommentREST(st, req, owner, name, num)
		}
	}
	return jsonError(404, "Not Found")
}

func listIssues(st *store, req *httpReq, owner, name string) *httpResp {
	state := req.queryGet("state")
	if state == "" {
		state = "open"
	}
	direction := req.queryGet("direction")
	if direction == "" {
		direction = "desc"
	}
	perPage := queryInt(req, "per_page", 30)
	if perPage < 1 {
		perPage = 30
	}
	page := queryInt(req, "page", 1)
	if page < 1 {
		page = 1
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	var nums []int
	for _, n := range r.issueOrder {
		is := r.issues[n]
		if state != "all" && is.state != state {
			continue
		}
		nums = append(nums, n)
	}
	if direction == "desc" {
		for i, j := 0, len(nums)-1; i < j; i, j = i+1, j-1 {
			nums[i], nums[j] = nums[j], nums[i]
		}
	}
	total := len(nums)
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	var b strings.Builder
	b.WriteString("[")
	for i := start; i < end; i++ {
		if i > start {
			b.WriteString(",")
		}
		b.Write(issueJSON(r, r.issues[nums[i]]))
	}
	b.WriteString("]")
	return jsonResp(200, []byte(b.String()))
}

func createIssueREST(st *store, req *httpReq, owner, name string) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	title := jstr(m["title"])
	if title == "" {
		return jsonError(422, "title is required")
	}
	var labelNames []string
	for _, lv := range jarr(m["labels"]) {
		labelNames = append(labelNames, jstr(lv))
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	for _, ln := range labelNames {
		if _, ok := r.labels[ln]; !ok {
			return jsonError(422, "Label does not exist")
		}
	}
	num := r.nextIssue
	r.nextIssue++
	is := &issue{
		id:     st.nextIssueID,
		number: num,
		title:  title,
		body:   jstr(m["body"]),
		state:  "open",
		labels: labelNames,
		seq:    int(st.reqSeq),
	}
	st.nextIssueID++
	st.reqSeq++
	r.issues[num] = is
	r.issueOrder = append(r.issueOrder, num)
	return jsonResp(201, issueJSON(r, is))
}

func getIssueREST(st *store, owner, name string, num int) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	is, ok := r.issues[num]
	if !ok {
		return jsonError(404, "Not Found")
	}
	return jsonResp(200, issueJSON(r, is))
}

func patchIssueREST(st *store, req *httpReq, owner, name string, num int) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	is, ok := r.issues[num]
	if !ok {
		return jsonError(404, "Not Found")
	}
	if v, ok := m["state"]; ok {
		is.state = jstr(v)
	}
	if v, ok := m["title"]; ok {
		is.title = jstr(v)
	}
	if v, ok := m["body"]; ok {
		is.body = jstr(v)
	}
	delete(r.issueJSON, num)
	return jsonResp(200, issueJSON(r, is))
}

func listComments(st *store, owner, name string, num int) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if _, ok := r.issues[num]; !ok {
		return jsonError(404, "Not Found")
	}
	var b strings.Builder
	b.WriteString("[")
	for i, c := range r.comments[num] {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(commentJSON(c))
	}
	b.WriteString("]")
	return jsonResp(200, []byte(b.String()))
}

func createCommentREST(st *store, req *httpReq, owner, name string, num int) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	commentBody := jstr(m["body"])
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if _, ok := r.issues[num]; !ok {
		return jsonError(404, "Not Found")
	}
	c := &comment{id: st.nextCommentID, body: commentBody}
	st.nextCommentID++
	r.comments[num] = append(r.comments[num], c)
	return jsonResp(201, []byte(commentJSON(c)))
}

// ---------------------------------------------------------------- pulls

func handlePulls(st *store, req *httpReq, owner, name string, rest []string) *httpResp {
	if len(rest) == 0 {
		if req.method == "GET" {
			return listPulls(st, req, owner, name)
		}
		if req.method == "POST" {
			return createPullREST(st, req, owner, name)
		}
		return jsonError(404, "Not Found")
	}
	num, err := strconv.Atoi(rest[0])
	if err != nil {
		return jsonError(404, "Not Found")
	}
	if len(rest) == 1 {
		if req.method == "GET" {
			return getPullREST(st, owner, name, num)
		}
		return jsonError(404, "Not Found")
	}
	if len(rest) == 2 && rest[1] == "files" && req.method == "GET" {
		return listPullFiles(st, owner, name, num)
	}
	if len(rest) == 2 && rest[1] == "merge" && req.method == "PUT" {
		return mergePullREST(st, req, owner, name, num)
	}
	return jsonError(404, "Not Found")
}

func listPulls(st *store, req *httpReq, owner, name string) *httpResp {
	state := req.queryGet("state")
	if state == "" {
		state = "open"
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	var b strings.Builder
	b.WriteString("[")
	first := true
	for _, n := range r.pullOrder {
		p := r.pulls[n]
		pstate := p.state
		if p.merged {
			pstate = "closed"
		}
		if state != "all" && pstate != state {
			continue
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		b.Write(pullJSON(r, p))
	}
	b.WriteString("]")
	return jsonResp(200, []byte(b.String()))
}

func createPullREST(st *store, req *httpReq, owner, name string) *httpResp {
	body, err := parseJSON(req.body)
	if err != nil {
		return jsonError(400, "Invalid JSON")
	}
	m := jobj(body)
	head := jstr(m["head"])
	base := jstr(m["base"])
	title := jstr(m["title"])
	if head == "" || base == "" || title == "" {
		return jsonError(422, "head, base and title are required")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	headSha, ok1 := r.refs["refs/heads/"+head]
	baseSha, ok2 := r.refs["refs/heads/"+base]
	if !ok1 || !ok2 {
		return jsonError(422, "head or base branch does not exist")
	}
	for _, n := range r.pullOrder {
		p := r.pulls[n]
		if p.headRef == head && p.state == "open" {
			return jsonError(422, "A pull request already exists for "+head)
		}
	}
	num := r.nextPull
	r.nextPull++
	p := &pull{
		id:      st.nextPullID,
		number:  num,
		title:   title,
		body:    jstr(m["body"]),
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
	return jsonResp(201, pullJSON(r, p))
}

func getPullREST(st *store, owner, name string, num int) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	p, ok := r.pulls[num]
	if !ok {
		return jsonError(404, "Not Found")
	}
	return jsonResp(200, pullJSON(r, p))
}

func listPullFiles(st *store, owner, name string, num int) *httpResp {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	p, ok := r.pulls[num]
	if !ok {
		return jsonError(404, "Not Found")
	}
	baseFiles := r.filesOfCommit(p.baseSha)
	headFiles := r.filesOfCommit(p.headSha)
	changes := r.diffFiles(baseFiles, headFiles)
	var b strings.Builder
	b.WriteString("[")
	for i, c := range changes {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"filename":`)
		b.WriteString(jsonString(c.path))
		b.WriteString(`,"status":`)
		b.WriteString(jsonString(c.status))
		b.WriteString(`,"additions":`)
		b.WriteString(itoa(c.additions))
		b.WriteString(`,"deletions":`)
		b.WriteString(itoa(c.deletions))
		b.WriteString(`,"changes":`)
		b.WriteString(itoa(c.additions + c.deletions))
		b.WriteString(`}`)
	}
	b.WriteString("]")
	return jsonResp(200, []byte(b.String()))
}

func mergePullREST(st *store, req *httpReq, owner, name string, num int) *httpResp {
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.getRepo(owner, name)
	if r == nil {
		return jsonError(404, "Not Found")
	}
	p, ok := r.pulls[num]
	if !ok {
		return jsonError(404, "Not Found")
	}
	if p.merged || p.state != "open" {
		return jsonError(405, "Pull request already merged")
	}
	baseTip, ok := r.refs["refs/heads/"+p.baseRef]
	if !ok {
		return jsonError(404, "Not Found")
	}
	// conflict: base moved AND head/base both changed the same file vs PR base
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
				return jsonError(405, "Merge conflict")
			}
		}
	}
	// squash: tree = head tip's tree, parent = base tip
	headCI := r.getCommit(p.headSha)
	if headCI == nil {
		return jsonError(404, "Not Found")
	}
	r.seq++
	mergeOID := r.putObject("commit", commitBytes(headCI.tree, []string{baseTip}, p.title, r.authorLine(), baseEpoch+int64(r.seq)*60))
	r.refs["refs/heads/"+p.baseRef] = mergeOID
	r.version++
	p.merged = true
	p.state = "closed"
	p.mergeSha = mergeOID
	p.mergedAt = tsFor(r.seq)
	return jsonResp(200, []byte(`{"sha":`+jsonString(mergeOID)+`,"merged":true,"message":`+jsonString(p.title)+`}`))
}
