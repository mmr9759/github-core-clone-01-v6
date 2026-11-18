package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

const zeroOID = "0000000000000000000000000000000000000000"

// ---------------------------------------------------------------- git objects

type gitObject struct {
	typ  string // commit | tree | blob | tag
	data []byte
}

func hashObject(typ string, data []byte) string {
	h := sha1.New()
	h.Write([]byte(typ))
	h.Write([]byte(" "))
	h.Write([]byte(itoa(len(data))))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

type commitInfo struct {
	tree      string
	parents   []string
	author    string
	committer string
	message   string
}

type treeEntry struct {
	mode string
	name string
	oid  string
}

func parseCommit(data []byte) *commitInfo {
	ci := &commitInfo{}
	blank := bytes.Index(data, []byte("\n\n"))
	headEnd := len(data)
	if blank >= 0 {
		headEnd = blank
		ci.message = string(data[blank+2:])
	}
	for _, line := range strings.Split(string(data[:headEnd]), "\n") {
		if line == "" {
			continue
		}
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		key, val := line[:sp], line[sp+1:]
		switch key {
		case "tree":
			ci.tree = val
		case "parent":
			ci.parents = append(ci.parents, val)
		case "author":
			ci.author = val
		case "committer":
			ci.committer = val
		}
	}
	return ci
}

func parseTree(data []byte) []treeEntry {
	var out []treeEntry
	off := 0
	for off < len(data) {
		sp := bytes.IndexByte(data[off:], ' ')
		if sp < 0 {
			break
		}
		nul := bytes.IndexByte(data[off+sp+1:], 0)
		if nul < 0 || off+sp+1+nul+21 > len(data) {
			break
		}
		mode := string(data[off : off+sp])
		name := string(data[off+sp+1 : off+sp+1+nul])
		oid := hex.EncodeToString(data[off+sp+1+nul+1 : off+sp+1+nul+21])
		out = append(out, treeEntry{mode: mode, name: name, oid: oid})
		off += sp + 1 + nul + 21
	}
	return out
}

func treeNameLess(a, b treeEntry) bool {
	an, bn := a.name, b.name
	if a.mode == "40000" {
		an += "/"
	}
	if b.mode == "40000" {
		bn += "/"
	}
	return an < bn
}

func buildTree(entries []treeEntry) []byte {
	sorted := append([]treeEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return treeNameLess(sorted[i], sorted[j]) })
	var buf bytes.Buffer
	for _, e := range sorted {
		buf.WriteString(e.mode)
		buf.WriteByte(' ')
		buf.WriteString(e.name)
		buf.WriteByte(0)
		raw, _ := hex.DecodeString(e.oid)
		buf.Write(raw)
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------- store model

type label struct {
	name  string
	color string
}

type issue struct {
	id     int
	number int
	title  string
	body   string
	state  string // open | closed
	labels []string
	seq    int
}

type comment struct {
	id   int
	body string
}

type pull struct {
	id        int
	number    int
	title     string
	body      string
	state     string // open | closed | merged
	headRef   string
	baseRef   string
	headSha   string
	baseSha   string
	merged    bool
	mergeSha  string
	mergedAt  string
	seq       int
}

type repo struct {
	id            int
	name          string
	owner         string
	desc          string
	priv          bool
	defaultBranch string
	refs          map[string]string
	objects       map[string]*gitObject
	commitCache   map[string]*commitInfo
	treeCache     map[string][]treeEntry
	labels        map[string]*label
	issues        map[int]*issue
	issueOrder    []int // numbers in creation order
	nextIssue     int
	pulls         map[int]*pull
	pullOrder     []int
	nextPull      int
	comments      map[int][]*comment
	version       uint64 // bumped on any ref/object change; invalidates pack caches
	seq           int    // creation sequence for deterministic timestamps
	issueJSON     map[int][]byte
}

type store struct {
	mu            sync.RWMutex
	repos         map[string]*repo
	nextRepoID    int
	nextIssueID   int
	nextCommentID int
	nextPullID    int
	reqSeq        uint64
	packCache     map[string]*packEntry
	packCacheKeys []string
}

func newStore() *store {
	return &store{
		repos:         map[string]*repo{},
		nextRepoID:    1,
		nextIssueID:   1,
		nextCommentID: 1,
		nextPullID:    1,
		packCache:     map[string]*packEntry{},
	}
}

const baseEpoch = 1735689600 // 2025-01-01T00:00:00Z

func tsFor(seq int) string {
	return time.Unix(baseEpoch+int64(seq)*60, 0).UTC().Format("2006-01-02T15:04:05Z")
}

func (s *store) getRepo(owner, name string) *repo {
	return s.repos[owner+"/"+name]
}

func (s *store) createRepo(owner, name, desc string, priv bool) (*repo, bool) {
	key := owner + "/" + name
	if _, ok := s.repos[key]; ok {
		return nil, false
	}
	r := &repo{
		id:            s.nextRepoID,
		name:          name,
		owner:         owner,
		desc:          desc,
		priv:          priv,
		defaultBranch: "main",
		refs:          map[string]string{},
		objects:       map[string]*gitObject{},
		commitCache:   map[string]*commitInfo{},
		treeCache:     map[string][]treeEntry{},
		labels:        map[string]*label{},
		issues:        map[int]*issue{},
		nextIssue:     1,
		pulls:         map[int]*pull{},
		nextPull:      1,
		comments:      map[int][]*comment{},
		issueJSON:     map[int][]byte{},
	}
	s.nextRepoID++
	s.repos[key] = r
	return r, true
}

func (s *store) deleteRepo(owner, name string) bool {
	key := owner + "/" + name
	if _, ok := s.repos[key]; !ok {
		return false
	}
	delete(s.repos, key)
	return true
}

func (s *store) viewerRepos() []*repo {
	var out []*repo
	for _, r := range s.repos {
		if r.owner == viewer {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// ---------------------------------------------------------------- object access (caller holds at least a read lock)

func (r *repo) putObject(typ string, data []byte) string {
	oid := hashObject(typ, data)
	if _, ok := r.objects[oid]; !ok {
		r.objects[oid] = &gitObject{typ: typ, data: data}
		r.version++
	}
	return oid
}

func (r *repo) getCommit(oid string) *commitInfo {
	if ci, ok := r.commitCache[oid]; ok {
		return ci
	}
	obj := r.objects[oid]
	if obj == nil || obj.typ != "commit" {
		return nil
	}
	ci := parseCommit(obj.data)
	r.commitCache[oid] = ci
	return ci
}

func (r *repo) getTree(oid string) []treeEntry {
	if e, ok := r.treeCache[oid]; ok {
		return e
	}
	obj := r.objects[oid]
	if obj == nil || obj.typ != "tree" {
		return nil
	}
	entries := parseTree(obj.data)
	r.treeCache[oid] = entries
	return entries
}

// flattenTree returns path -> blob oid for every file under the tree.
func (r *repo) flattenTree(treeOID string, prefix string, out map[string]string) {
	for _, e := range r.getTree(treeOID) {
		path := e.name
		if prefix != "" {
			path = prefix + "/" + e.name
		}
		switch e.mode {
		case "40000":
			r.flattenTree(e.oid, path, out)
		case "160000":
			// gitlink: not a file we hold
		default:
			out[path] = e.oid
		}
	}
}

func (r *repo) filesOfCommit(commitOID string) map[string]string {
	ci := r.getCommit(commitOID)
	if ci == nil {
		return map[string]string{}
	}
	out := map[string]string{}
	r.flattenTree(ci.tree, "", out)
	return out
}

// buildTreeFromFiles builds nested trees for path->blobOid and stores them.
func (r *repo) buildTreeFromFiles(files map[string]string) string {
	type node struct {
		files map[string]string
		dirs  map[string]*node
	}
	mk := func() *node { return &node{files: map[string]string{}, dirs: map[string]*node{}} }
	root := mk()
	for path, oid := range files {
		parts := strings.Split(path, "/")
		n := root
		for _, seg := range parts[:len(parts)-1] {
			if _, ok := n.dirs[seg]; !ok {
				n.dirs[seg] = mk()
			}
			n = n.dirs[seg]
		}
		n.files[parts[len(parts)-1]] = oid
	}
	var build func(n *node) string
	build = func(n *node) string {
		var entries []treeEntry
		for name, oid := range n.files {
			entries = append(entries, treeEntry{mode: "100644", name: name, oid: oid})
		}
		for name, child := range n.dirs {
			entries = append(entries, treeEntry{mode: "40000", name: name, oid: build(child)})
		}
		return r.putObject("tree", buildTree(entries))
	}
	return build(root)
}

func commitBytes(tree string, parents []string, message, author string, ts int64) []byte {
	var b bytes.Buffer
	b.WriteString("tree " + tree + "\n")
	for _, p := range parents {
		b.WriteString("parent " + p + "\n")
	}
	who := author + " " + itoa(int(ts)) + " +0000"
	b.WriteString("author " + who + "\n")
	b.WriteString("committer " + who + "\n")
	b.WriteString("\n")
	b.WriteString(message)
	if !strings.HasSuffix(message, "\n") {
		b.WriteString("\n")
	}
	return b.Bytes()
}

func (r *repo) authorLine() string {
	return viewer + " <" + viewer + "@example.invalid>"
}

// commitFile creates (or updates) one file on branch and returns the new commit oid.
// When branch does not exist it is created from the default branch tip.
func (r *repo) commitFile(branch, path string, content []byte, message string) (commitOID, blobOID string) {
	blobOID = r.putObject("blob", content)
	ref := "refs/heads/" + branch
	tip, hasTip := r.refs[ref]
	files := map[string]string{}
	var parents []string
	if hasTip {
		parents = []string{tip}
		files = r.filesOfCommit(tip)
	} else if branch != r.defaultBranch {
		// new branch forks the default branch tip when there is one
		if dt, ok := r.refs["refs/heads/"+r.defaultBranch]; ok {
			parents = []string{dt}
			files = r.filesOfCommit(dt)
		}
	}
	files[path] = blobOID
	treeOID := r.buildTreeFromFiles(files)
	r.seq++
	commitOID = r.putObject("commit", commitBytes(treeOID, parents, message, r.authorLine(), baseEpoch+int64(r.seq)*60))
	r.refs[ref] = commitOID
	r.version++
	return commitOID, blobOID
}

func (r *repo) deleteFile(branch, path, message string) (string, bool) {
	ref := "refs/heads/" + branch
	tip, ok := r.refs[ref]
	if !ok {
		return "", false
	}
	files := r.filesOfCommit(tip)
	if _, ok := files[path]; !ok {
		return "", false
	}
	delete(files, path)
	treeOID := r.buildTreeFromFiles(files)
	r.seq++
	commitOID := r.putObject("commit", commitBytes(treeOID, []string{tip}, message, r.authorLine(), baseEpoch+int64(r.seq)*60))
	r.refs[ref] = commitOID
	r.version++
	return commitOID, true
}

// resolveRef turns a branch name, full ref, or commit oid into a commit oid.
func (r *repo) resolveRef(ref string) (string, bool) {
	if oid, ok := r.refs[ref]; ok {
		return oid, true
	}
	if oid, ok := r.refs["refs/heads/"+ref]; ok {
		return oid, true
	}
	if oid, ok := r.refs["refs/tags/"+ref]; ok {
		if obj := r.objects[oid]; obj != nil && obj.typ == "tag" {
			if tag := parseTag(obj.data); tag != "" {
				return tag, true
			}
		}
		return oid, true
	}
	if len(ref) == 40 {
		if obj := r.objects[ref]; obj != nil && obj.typ == "commit" {
			return ref, true
		}
	}
	return "", false
}

func parseTag(data []byte) string {
	blank := bytes.Index(data, []byte("\n\n"))
	head := data
	if blank >= 0 {
		head = data[:blank]
	}
	for _, line := range strings.Split(string(head), "\n") {
		if strings.HasPrefix(line, "object ") {
			return strings.TrimPrefix(line, "object ")
		}
	}
	return ""
}

// history returns commit oids from tip following first parents, newest first.
func (r *repo) history(tip string) []string {
	var out []string
	seen := map[string]bool{}
	cur := tip
	for cur != "" && !seen[cur] {
		seen[cur] = true
		ci := r.getCommit(cur)
		if ci == nil {
			break
		}
		out = append(out, cur)
		if len(ci.parents) == 0 {
			break
		}
		cur = ci.parents[0]
	}
	return out
}

// isAncestor reports whether anc is reachable from oid (following parents).
func (r *repo) isAncestor(anc, oid string) bool {
	if anc == oid {
		return true
	}
	seen := map[string]bool{}
	queue := []string{oid}
	for len(queue) > 0 {
		cur := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		ci := r.getCommit(cur)
		if ci == nil {
			continue
		}
		for _, p := range ci.parents {
			if p == anc {
				return true
			}
			queue = append(queue, p)
		}
	}
	return false
}

type fileChange struct {
	path       string
	status     string // added | removed | modified
	oldOID     string
	newOID     string
	additions  int
	deletions  int
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if !bytes.HasSuffix(data, []byte("\n")) {
		n++
	}
	return n
}

// diffFiles compares two file maps and returns the changed files.
func (r *repo) diffFiles(base, head map[string]string) []fileChange {
	var out []fileChange
	for path, newOID := range head {
		oldOID, ok := base[path]
		if !ok {
			n := 0
			if obj := r.objects[newOID]; obj != nil {
				n = countLines(obj.data)
			}
			out = append(out, fileChange{path: path, status: "added", newOID: newOID, additions: n})
			continue
		}
		if oldOID == newOID {
			continue
		}
		add, del := 0, 0
		if oldObj := r.objects[oldOID]; oldObj != nil {
			del = countLines(oldObj.data)
		}
		if newObj := r.objects[newOID]; newObj != nil {
			add = countLines(newObj.data)
		}
		// multiset line difference keeps the counts honest for small edits
		if oldObj := r.objects[oldOID]; oldObj != nil {
			if newObj := r.objects[newOID]; newObj != nil {
				add, del = lineDiff(oldObj.data, newObj.data)
			}
		}
		out = append(out, fileChange{path: path, status: "modified", oldOID: oldOID, newOID: newOID, additions: add, deletions: del})
	}
	for path, oldOID := range base {
		if _, ok := head[path]; !ok {
			n := 0
			if obj := r.objects[oldOID]; obj != nil {
				n = countLines(obj.data)
			}
			out = append(out, fileChange{path: path, status: "removed", oldOID: oldOID, deletions: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func lineDiff(oldData, newData []byte) (add, del int) {
	oldLines := strings.Split(string(oldData), "\n")
	newLines := strings.Split(string(newData), "\n")
	counts := map[string]int{}
	for _, l := range oldLines {
		counts[l]++
	}
	for _, l := range newLines {
		if counts[l] > 0 {
			counts[l]--
		} else {
			add++
		}
	}
	for _, c := range counts {
		del += c
	}
	return add, del
}
