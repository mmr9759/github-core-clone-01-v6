package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
)

// ---------------------------------------------------------------- request / response

type httpReq struct {
	method  string
	path    string
	query   string
	headers map[string]string
	body    []byte
}

func (r *httpReq) header(name string) string {
	return r.headers[strings.ToLower(name)]
}

func (r *httpReq) queryGet(name string) string {
	q := r.query
	for len(q) > 0 {
		amp := strings.IndexByte(q, '&')
		var pair string
		if amp < 0 {
			pair = q
			q = ""
		} else {
			pair = q[:amp]
			q = q[amp+1:]
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		if pair[:eq] == name {
			return urlUnescape(pair[eq+1:])
		}
	}
	return ""
}

func urlUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) {
			hi := hexVal(s[i+1])
			lo := hexVal(s[i+2])
			if hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		if c == '+' {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func urlUnescapePath(s string) string {
	// path segments: '+' stays a plus
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) {
			hi := hexVal(s[i+1])
			lo := hexVal(s[i+2])
			if hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

type httpResp struct {
	status      int
	contentType string
	headers     [][2]string
	body        []byte
}

func jsonError(status int, msg string) *httpResp {
	return &httpResp{
		status:      status,
		contentType: "application/json",
		body:        []byte(`{"message":` + jsonString(msg) + `}`),
	}
}

func jsonResp(status int, body []byte) *httpResp {
	return &httpResp{status: status, contentType: "application/json", body: body}
}

var reqCounter uint64

func nextReqID() string {
	n := atomic.AddUint64(&reqCounter, 1)
	return "forge-" + itoa(int(n))
}

// ---------------------------------------------------------------- request parsing

func readRequest(br *bufio.Reader) (*httpReq, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("bad request line")
	}
	req := &httpReq{method: parts[0], headers: map[string]string{}}
	target := parts[1]
	if idx := strings.IndexByte(target, '?'); idx >= 0 {
		req.path = urlUnescapePath(target[:idx])
		req.query = target[idx+1:]
	} else {
		req.path = urlUnescapePath(target)
	}
	for {
		hline, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hline = strings.TrimRight(hline, "\r\n")
		if hline == "" {
			break
		}
		colon := strings.IndexByte(hline, ':')
		if colon < 0 {
			continue
		}
		req.headers[strings.ToLower(strings.TrimSpace(hline[:colon]))] = strings.TrimSpace(hline[colon+1:])
	}
	// body
	if strings.EqualFold(req.header("Transfer-Encoding"), "chunked") {
		body, err := readChunked(br)
		if err != nil {
			return nil, err
		}
		req.body = body
	} else if cl := req.header("Content-Length"); cl != "" {
		n, err := strconv.Atoi(strings.TrimSpace(cl))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad content-length")
		}
		if n > 64<<20 {
			return nil, fmt.Errorf("body too large")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
		req.body = body
	}
	return req, nil
}

func readChunked(br *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeLine = strings.TrimSpace(sizeLine)
		if idx := strings.IndexByte(sizeLine, ';'); idx >= 0 {
			sizeLine = sizeLine[:idx]
		}
		n, err := strconv.ParseInt(sizeLine, 16, 64)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			// trailing headers until blank line
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return nil, err
				}
				if strings.TrimRight(l, "\r\n") == "" {
					break
				}
			}
			return out, nil
		}
		chunk := make([]byte, n+2)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, err
		}
		out = append(out, chunk[:n]...)
	}
}

// ---------------------------------------------------------------- response writing

var reasonPhrases = map[int]string{
	200: "OK", 201: "Created", 204: "No Content", 400: "Bad Request",
	401: "Unauthorized", 404: "Not Found", 405: "Method Not Allowed",
	409: "Conflict", 422: "Unprocessable Entity",
}

func writeResp(conn net.Conn, resp *httpResp) error {
	reason, ok := reasonPhrases[resp.status]
	if !ok {
		reason = "Status"
	}
	var b bytes.Buffer
	b.WriteString("HTTP/1.1 " + itoa(resp.status) + " " + reason + "\r\n")
	ct := resp.contentType
	if ct == "" {
		ct = "application/json"
	}
	b.WriteString("Content-Type: " + ct + "\r\n")
	b.WriteString("Content-Length: " + itoa(len(resp.body)) + "\r\n")
	b.WriteString("X-GitHub-Request-Id: " + nextReqID() + "\r\n")
	for _, h := range resp.headers {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("Connection: keep-alive\r\n")
	b.WriteString("\r\n")
	b.Write(resp.body)
	_, err := conn.Write(b.Bytes())
	return err
}

// ---------------------------------------------------------------- routing

func route(st *store, req *httpReq) *httpResp {
	path := req.path
	if req.method == "GET" && path == "/healthz" {
		return jsonResp(200, []byte(`{"ok":true}`))
	}
	if strings.HasPrefix(path, "/api/v3/") {
		if !checkAuth(req) {
			return jsonError(401, "Requires authentication")
		}
		return handleREST(st, req)
	}
	if path == "/api/graphql" && req.method == "POST" {
		if !checkAuth(req) {
			return jsonError(401, "Requires authentication")
		}
		return handleGraphQL(st, req)
	}
	if path == "/mcp" {
		if !checkAuth(req) {
			return jsonError(401, "Requires authentication")
		}
		if req.method == "POST" {
			return handleMCP(st, req)
		}
		if req.method == "DELETE" {
			return &httpResp{status: 204, contentType: "application/json", body: nil}
		}
		return jsonError(404, "Not Found")
	}
	// git smart HTTP: /{owner}/{repo}/info/refs, git-upload-pack, git-receive-pack
	// (git clients append ".git" to the repo name; GitHub accepts both)
	segs := splitPath(path)
	if len(segs) == 4 && segs[2] == "info" && segs[3] == "refs" && req.method == "GET" {
		return handleInfoRefs(st, req, segs[0], strings.TrimSuffix(segs[1], ".git"), req.queryGet("service"))
	}
	if len(segs) == 3 && segs[2] == "git-upload-pack" && req.method == "POST" {
		return handleUploadPack(st, req, segs[0], strings.TrimSuffix(segs[1], ".git"))
	}
	if len(segs) == 3 && segs[2] == "git-receive-pack" && req.method == "POST" {
		return handleReceivePack(st, req, segs[0], strings.TrimSuffix(segs[1], ".git"))
	}
	return jsonError(404, "Not Found")
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func checkAuth(req *httpReq) bool {
	if shopToken == "" {
		return req.header("Authorization") != ""
	}
	auth := req.header("Authorization")
	if auth == "" {
		return false
	}
	if auth == shopToken {
		return true
	}
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == shopToken {
		return true
	}
	if strings.HasPrefix(auth, "token ") && strings.TrimPrefix(auth, "token ") == shopToken {
		return true
	}
	return false
}

// ---------------------------------------------------------------- env client (register / done)

func envRequest(method, url, body string) ([]byte, int, error) {
	// url is http://host:port/path
	rest := strings.TrimPrefix(url, "http://")
	host := rest
	path := "/"
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		host = rest[:idx]
		path = rest[idx:]
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	var b bytes.Buffer
	b.WriteString(method + " " + path + " HTTP/1.1\r\n")
	b.WriteString("Host: " + host + "\r\n")
	b.WriteString("Content-Type: application/json\r\n")
	b.WriteString("Content-Length: " + itoa(len(body)) + "\r\n")
	b.WriteString("Connection: close\r\n\r\n")
	b.WriteString(body)
	if _, err := conn.Write(b.Bytes()); err != nil {
		return nil, 0, err
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return nil, 0, err
	}
	status := 0
	fields := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(fields) >= 2 {
		status, _ = strconv.Atoi(fields[1])
	}
	// headers
	contentLength := -1
	chunked := false
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return nil, status, err
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		colon := strings.IndexByte(l, ':')
		if colon < 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(l[:colon]))
		v := strings.TrimSpace(l[colon+1:])
		if k == "content-length" {
			contentLength, _ = strconv.Atoi(v)
		}
		if k == "transfer-encoding" && strings.Contains(strings.ToLower(v), "chunked") {
			chunked = true
		}
	}
	var respBody []byte
	if chunked {
		respBody, err = readChunked(br)
		if err != nil {
			return nil, status, err
		}
	} else if contentLength >= 0 {
		respBody = make([]byte, contentLength)
		if _, err := io.ReadFull(br, respBody); err != nil {
			return nil, status, err
		}
	} else {
		respBody, err = io.ReadAll(br)
		if err != nil {
			return nil, status, err
		}
	}
	return respBody, status, nil
}

func serveConn(conn net.Conn, st *store) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 65536)
	for {
		req, err := readRequest(br)
		if err != nil {
			return
		}
		resp := route(st, req)
		if err := writeResp(conn, resp); err != nil {
			return
		}
	}
}
