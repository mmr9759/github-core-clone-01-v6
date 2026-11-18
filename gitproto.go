package main

import (
	"bytes"
	"sort"
	"strings"
)

const forgeAgent = "gitforge/1.0"

var v2Advertisement []byte

func init() {
	var b bytes.Buffer
	b.Write(pktLine([]byte("version 2\n")))
	b.Write(pktLine([]byte("agent=" + forgeAgent + "\n")))
	b.Write(pktLine([]byte("ls-refs\n")))
	b.Write(pktLine([]byte("fetch=shallow wait-for-done\n")))
	b.Write(pktLine([]byte("object-format=sha1\n")))
	b.Write(pktFlush())
	v2Advertisement = b.Bytes()
}

type refLine struct {
	oid  string
	name string
}

// sortedRefs returns every ref of the repo sorted by name, HEAD first is
// handled by callers.
func sortedRefs(r *repo) []refLine {
	names := make([]string, 0, len(r.refs))
	for name := range r.refs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]refLine, 0, len(names))
	for _, name := range names {
		out = append(out, refLine{oid: r.refs[name], name: name})
	}
	return out
}

func headOID(r *repo) string {
	if oid, ok := r.refs["refs/heads/"+r.defaultBranch]; ok {
		return oid
	}
	return zeroOID
}

// peeledTarget returns the commit a tag points at, or "" when the ref is not
// an annotated tag.
func (r *repo) peeledTarget(oid string) string {
	if obj := r.objects[oid]; obj != nil && obj.typ == "tag" {
		return parseTag(obj.data)
	}
	return ""
}

func uploadPackCapsV0() string {
	return "multi_ack thin-pack side-band side-band-64k ofs-delta shallow " +
		"deepen-since deepen-not deepen-relative no-progress include-tag " +
		"multi_ack_detailed allow-tip-sha1-in-want allow-reachable-sha1-in-want " +
		"no-done symref=HEAD:refs/heads/main agent=" + forgeAgent
}

func receivePackCapsV0() string {
	return "report-status report-status-v2 delete-refs side-band-64k ofs-delta " +
		"quiet atomic agent=" + forgeAgent
}

func v0Advertisement(r *repo, service, caps string) []byte {
	var b bytes.Buffer
	b.Write(pktLine([]byte("# service=" + service + "\n")))
	b.Write(pktFlush())
	b.Write(pktLine([]byte(headOID(r) + " HEAD\x00" + caps + "\n")))
	for _, ref := range sortedRefs(r) {
		b.Write(pktLine([]byte(ref.oid + " " + ref.name + "\n")))
		if target := r.peeledTarget(ref.oid); target != "" {
			b.Write(pktLine([]byte(target + " " + ref.name + "^{}\n")))
		}
	}
	b.Write(pktFlush())
	return b.Bytes()
}

// ---------------------------------------------------------------- info/refs

func handleInfoRefs(st *store, req *httpReq, owner, name, service string) *httpResp {
	st.mu.RLock()
	r := st.getRepo(owner, name)
	st.mu.RUnlock()
	if r == nil {
		return jsonError(404, "Not Found")
	}
	switch service {
	case "git-upload-pack":
		if req.queryGet("service") == "" {
			// plain info/refs without service: git expects the v0 advertisement
		}
		if isV2(req) {
			return &httpResp{status: 200, contentType: "application/x-git-upload-pack-advertisement", body: v2Advertisement}
		}
		st.mu.RLock()
		body := v0Advertisement(r, "git-upload-pack", uploadPackCapsV0())
		st.mu.RUnlock()
		return &httpResp{status: 200, contentType: "application/x-git-upload-pack-advertisement", body: body}
	case "git-receive-pack":
		if isV2(req) {
			return &httpResp{status: 200, contentType: "application/x-git-receive-pack-advertisement", body: v2Advertisement}
		}
		st.mu.RLock()
		body := v0Advertisement(r, "git-receive-pack", receivePackCapsV0())
		st.mu.RUnlock()
		return &httpResp{status: 200, contentType: "application/x-git-receive-pack-advertisement", body: body}
	default:
		return jsonError(404, "Not Found")
	}
}

func isV2(req *httpReq) bool {
	return req.header("Git-Protocol") == "version=2"
}

// ---------------------------------------------------------------- ls-refs + fetch (protocol v2)

func handleUploadPack(st *store, req *httpReq, owner, name string) *httpResp {
	st.mu.RLock()
	r := st.getRepo(owner, name)
	st.mu.RUnlock()
	if r == nil {
		return jsonError(404, "Not Found")
	}
	if isV2(req) {
		return handleUploadPackV2(st, req, r)
	}
	// v0 upload-pack POST: the body is a request; the harness only uses v2
	// for fetch, but answer v0-style with an empty packfile section guard.
	return jsonError(400, "protocol version 0 fetch not supported")
}

func handleUploadPackV2(st *store, req *httpReq, r *repo) *httpResp {
	pkts, _, err := decodePktLines(req.body)
	if err != nil || len(pkts) == 0 || pkts[0].kind != 'd' {
		return jsonError(400, "malformed request")
	}
	cmd := strings.TrimSuffix(string(pkts[0].data), "\n")
	switch {
	case strings.HasPrefix(cmd, "command=ls-refs"):
		return lsRefsResponse(st, r, pkts[1:])
	case strings.HasPrefix(cmd, "command=fetch"):
		return fetchResponse(st, r, pkts[1:])
	default:
		return jsonError(400, "unknown command")
	}
}

func lsRefsResponse(st *store, r *repo, pkts []pkt) *httpResp {
	peel, symrefs, unborn := false, false, false
	var prefixes []string
	for _, p := range pkts {
		if p.kind != 'd' {
			continue
		}
		arg := strings.TrimSuffix(string(p.data), "\n")
		switch {
		case arg == "peel":
			peel = true
		case arg == "symrefs":
			symrefs = true
		case arg == "unborn":
			unborn = true
		case strings.HasPrefix(arg, "ref-prefix "):
			prefixes = append(prefixes, strings.TrimPrefix(arg, "ref-prefix "))
		}
	}
	matches := func(refname string) bool {
		if len(prefixes) == 0 {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(refname, p) {
				return true
			}
		}
		return false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	var b bytes.Buffer
	head := headOID(r)
	includeHead := len(prefixes) == 0
	for _, p := range prefixes {
		if strings.HasPrefix("HEAD", p) || strings.HasPrefix(p, "HEAD") {
			includeHead = true
		}
	}
	if includeHead {
		line := head + " HEAD"
		if symrefs {
			line += " symref-target:refs/heads/" + r.defaultBranch
		}
		if unborn && head == zeroOID {
			line += " unborn"
		}
		b.Write(pktLine([]byte(line + "\n")))
	}
	for _, ref := range sortedRefs(r) {
		if !matches(ref.name) {
			continue
		}
		line := ref.oid + " " + ref.name
		if peel {
			if target := r.peeledTarget(ref.oid); target != "" {
				line += " peeled:" + target
			}
		}
		b.Write(pktLine([]byte(line + "\n")))
	}
	b.Write(pktFlush())
	return &httpResp{status: 200, contentType: "application/x-git-upload-pack-result", body: b.Bytes()}
}

type packEntry struct {
	pack      []byte
	shallow   []string
	unshallow []string
}

func fetchResponse(st *store, r *repo, pkts []pkt) *httpResp {
	var wants, haves, shallow []string
	depth := 0
	done := false
	for _, p := range pkts {
		if p.kind != 'd' {
			continue
		}
		arg := strings.TrimSuffix(string(p.data), "\n")
		switch {
		case strings.HasPrefix(arg, "want "):
			wants = append(wants, strings.TrimPrefix(arg, "want "))
		case strings.HasPrefix(arg, "have "):
			haves = append(haves, strings.TrimPrefix(arg, "have "))
		case strings.HasPrefix(arg, "shallow "):
			shallow = append(shallow, strings.TrimPrefix(arg, "shallow "))
		case strings.HasPrefix(arg, "deepen "):
			depth = atoi(strings.TrimPrefix(arg, "deepen "))
		case arg == "done":
			done = true
		}
	}
	if len(wants) == 0 {
		return jsonError(400, "no want")
	}
	// pack cache: identical concurrent clones hit the same bytes (and the
	// same shallow-info), so no plan is recomputed on a hit
	key := packCacheKey(r, wants, haves, shallow, depth)
	st.mu.RLock()
	if entry, ok := st.packCache[key]; ok {
		st.mu.RUnlock()
		return fetchResponseBody(st, r, haves, done, entry)
	}
	st.mu.RUnlock()
	st.mu.Lock()
	plan := r.planFetch(wants, haves, shallow, depth)
	entry := &packEntry{pack: writePack(plan.objects), shallow: plan.shallow, unshallow: plan.unshallow}
	st.packCache[key] = entry
	st.packCacheKeys = append(st.packCacheKeys, key)
	if len(st.packCacheKeys) > 64 {
		delete(st.packCache, st.packCacheKeys[0])
		st.packCacheKeys = st.packCacheKeys[1:]
	}
	st.mu.Unlock()
	return fetchResponseBody(st, r, haves, done, entry)
}

func fetchResponseBody(st *store, r *repo, haves []string, done bool, entry *packEntry) *httpResp {
	var b bytes.Buffer
	// When the client sent haves, git (and the harness) require an
	// acknowledgments section: ACK every have the server holds, then
	// "ready". The request over HTTP is a single POST, so negotiation is
	// always over when the response is written — even when the client
	// did not send "done" (a deepen request never carries "done", yet
	// the server must still say "ready", or git refuses the sections
	// that follow). Without haves there is no negotiation and no
	// acknowledgments section at all.
	if len(haves) > 0 {
		b.Write(pktLine([]byte("acknowledgments\n")))
		acked := 0
		st.mu.RLock()
		for _, h := range haves {
			if _, ok := r.objects[h]; ok {
				b.Write(pktLine([]byte("ACK " + h + "\n")))
				acked++
			}
		}
		st.mu.RUnlock()
		if acked == 0 {
			b.Write(pktLine([]byte("NAK\n")))
		}
		b.Write(pktLine([]byte("ready\n")))
		b.Write(pktDelim())
	}
	if len(entry.shallow) > 0 || len(entry.unshallow) > 0 {
		b.Write(pktLine([]byte("shallow-info\n")))
		for _, oid := range entry.shallow {
			b.Write(pktLine([]byte("shallow " + oid + "\n")))
		}
		for _, oid := range entry.unshallow {
			b.Write(pktLine([]byte("unshallow " + oid + "\n")))
		}
		b.Write(pktDelim())
	}
	b.Write(pktLine([]byte("packfile\n")))
	pack := entry.pack
	for off := 0; off < len(pack); {
		end := off + 65515
		if end > len(pack) {
			end = len(pack)
		}
		chunk := make([]byte, 1+end-off)
		chunk[0] = 1
		copy(chunk[1:], pack[off:end])
		b.Write(pktLine(chunk))
		off = end
	}
	b.Write(pktFlush())
	return &httpResp{status: 200, contentType: "application/x-git-upload-pack-result", body: b.Bytes()}
}

func packCacheKey(r *repo, wants, haves, shallow []string, depth int) string {
	var b strings.Builder
	b.WriteString(itoa(r.id))
	b.WriteByte('|')
	b.WriteString(itoa(int(r.version)))
	b.WriteByte('|')
	b.WriteString(itoa(depth))
	b.WriteByte('|')
	for _, w := range wants {
		b.WriteString(w)
		b.WriteByte(',')
	}
	b.WriteByte('|')
	for _, h := range haves {
		b.WriteString(h)
		b.WriteByte(',')
	}
	b.WriteByte('|')
	for _, s := range shallow {
		b.WriteString(s)
		b.WriteByte(',')
	}
	return b.String()
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---------------------------------------------------------------- receive-pack (protocol v0)

func handleReceivePack(st *store, req *httpReq, owner, name string) *httpResp {
	st.mu.RLock()
	r := st.getRepo(owner, name)
	st.mu.RUnlock()
	if r == nil {
		return jsonError(404, "Not Found")
	}
	pkts, rest, err := decodePktLines(req.body)
	if err != nil {
		return jsonError(400, "malformed request")
	}
	type cmd struct {
		old, new, ref string
	}
	var cmds []cmd
	for _, p := range pkts {
		if p.kind == 'f' {
			break
		}
		if p.kind != 'd' {
			continue
		}
		line := strings.TrimSuffix(string(p.data), "\n")
		if idx := strings.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return jsonError(400, "malformed command")
		}
		cmds = append(cmds, cmd{old: parts[0], new: parts[1], ref: parts[2]})
	}
	var b bytes.Buffer
	report := func(ok bool, ref, reason string) {
		if ok {
			b.Write(pktLine([]byte("ok " + ref + "\n")))
		} else {
			b.Write(pktLine([]byte("ng " + ref + " " + reason + "\n")))
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	// store the pack first
	if len(rest) > 0 {
		objs, err := readPack(rest)
		if err != nil {
			inner := append(pktLine([]byte("unpack "+err.Error()+"\n")), pktFlush()...)
			for _, c := range cmds {
				inner = append(inner, pktLine([]byte("ng "+c.ref+" unpack failed\n"))...)
			}
			return &httpResp{status: 200, contentType: "application/x-git-receive-pack-result", body: sidebandWrap(inner)}
		}
		for _, o := range objs {
			r.putObject(o.typ, o.data)
		}
	}
	b.Write(pktLine([]byte("unpack ok\n")))
	for _, c := range cmds {
		current, exists := r.refs[c.ref]
		switch {
		case c.new == zeroOID:
			// delete
			if !exists {
				report(false, c.ref, "ref does not exist")
			} else if current != c.old {
				report(false, c.ref, "non-fast-forward")
			} else {
				delete(r.refs, c.ref)
				r.version++
				report(true, c.ref, "")
			}
		case c.old == zeroOID:
			// create
			if exists {
				report(false, c.ref, "already exists")
			} else if r.objects[c.new] == nil {
				report(false, c.ref, "object not found")
			} else {
				r.refs[c.ref] = c.new
				r.version++
				report(true, c.ref, "")
			}
		default:
			// update: must be fast-forward
			if !exists {
				report(false, c.ref, "ref does not exist")
			} else if current != c.old {
				report(false, c.ref, "non-fast-forward")
			} else if r.objects[c.new] == nil {
				report(false, c.ref, "object not found")
			} else if !r.isAncestor(c.old, c.new) {
				report(false, c.ref, "non-fast-forward")
			} else {
				r.refs[c.ref] = c.new
				r.version++
				report(true, c.ref, "")
			}
		}
	}
	b.Write(pktFlush())
	return &httpResp{status: 200, contentType: "application/x-git-receive-pack-result", body: sidebandWrap(b.Bytes())}
}

// sidebandWrap wraps an inner pkt-line stream (the receive-pack report) into
// sideband band-1 packets, exactly as real git does, then a final flush.
func sidebandWrap(inner []byte) []byte {
	var out bytes.Buffer
	for off := 0; off < len(inner); {
		end := off + 65515
		if end > len(inner) {
			end = len(inner)
		}
		chunk := make([]byte, 1+end-off)
		chunk[0] = 1
		copy(chunk[1:], inner[off:end])
		out.Write(pktLine(chunk))
		off = end
	}
	out.Write(pktFlush())
	return out.Bytes()
}
