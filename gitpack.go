package main

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sort"
)

// ---------------------------------------------------------------- pkt-line

func pktLine(payload []byte) []byte {
	out := make([]byte, 4+len(payload))
	n := len(payload) + 4
	out[0] = "0123456789abcdef"[n>>12&0xf]
	out[1] = "0123456789abcdef"[n>>8&0xf]
	out[2] = "0123456789abcdef"[n>>4&0xf]
	out[3] = "0123456789abcdef"[n&0xf]
	copy(out[4:], payload)
	return out
}

func pktFlush() []byte { return []byte("0000") }
func pktDelim() []byte { return []byte("0001") }

type pkt struct {
	kind byte // 'd' data, 'f' flush, '1' delim, '2' response-end
	data []byte
}

// decodePktLines parses a buffer into packets. It stops at the first flush
// packet and returns any bytes after it untouched (a receive-pack request
// carries its raw packfile after the flush). A body that is not pkt-line
// framed at all is an error.
func decodePktLines(buf []byte) ([]pkt, []byte, error) {
	var out []pkt
	off := 0
	for off < len(buf) {
		if len(buf)-off < 4 {
			return out, buf[off:], errors.New("truncated pkt-line")
		}
		hdr := string(buf[off : off+4])
		if hdr == "0000" {
			out = append(out, pkt{kind: 'f'})
			off += 4
			return out, buf[off:], nil
		}
		if hdr == "0001" {
			out = append(out, pkt{kind: '1'})
			off += 4
			continue
		}
		if hdr == "0002" {
			out = append(out, pkt{kind: '2'})
			off += 4
			continue
		}
		var n int
		for _, c := range hdr {
			n <<= 4
			switch {
			case c >= '0' && c <= '9':
				n |= int(c - '0')
			case c >= 'a' && c <= 'f':
				n |= int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				n |= int(c-'A') + 10
			default:
				return out, buf[off:], errors.New("bad pkt-line header")
			}
		}
		if n < 4 || off+n > len(buf) {
			return out, buf[off:], errors.New("bad pkt-line length")
		}
		out = append(out, pkt{kind: 'd', data: buf[off+4 : off+n]})
		off += n
	}
	return out, nil, nil
}

// ---------------------------------------------------------------- packfile

// writePack builds a version-2 pack from plain objects (no deltas).
func writePack(objs []*gitObject) []byte {
	var body bytes.Buffer
	hdr := make([]byte, 12)
	copy(hdr, "PACK")
	binary.BigEndian.PutUint32(hdr[4:], 2)
	binary.BigEndian.PutUint32(hdr[8:], uint32(len(objs)))
	body.Write(hdr)
	for _, o := range objs {
		body.Write(packObjectHeader(o.typ, len(o.data)))
		w, _ := zlib.NewWriterLevel(&body, zlib.BestSpeed)
		w.Write(o.data)
		w.Close()
	}
	sum := sha1.Sum(body.Bytes())
	out := body.Bytes()
	out = append(out, sum[:]...)
	return out
}

func packObjectHeader(typ string, size int) []byte {
	var code int
	switch typ {
	case "commit":
		code = 1
	case "tree":
		code = 2
	case "blob":
		code = 3
	case "tag":
		code = 4
	}
	var out []byte
	first := byte(code<<4) | byte(size&0x0f)
	size >>= 4
	for size > 0 {
		out = append(out, first|0x80)
		first = byte(size & 0x7f)
		size >>= 7
	}
	out = append(out, first)
	return out
}

// readPack parses a packfile, verifies its trailer, and returns its objects.
func readPack(pack []byte) ([]*gitObject, error) {
	if len(pack) < 32 || string(pack[:4]) != "PACK" {
		return nil, errors.New("bad pack header")
	}
	version := binary.BigEndian.Uint32(pack[4:])
	if version != 2 && version != 3 {
		return nil, errors.New("unsupported pack version")
	}
	count := binary.BigEndian.Uint32(pack[8:])
	trailerStart := len(pack) - 20
	sum := sha1.Sum(pack[:trailerStart])
	if !bytes.Equal(sum[:], pack[trailerStart:]) {
		return nil, errors.New("pack checksum mismatch")
	}
	type rawEntry struct {
		offset int
		typ    string
		size   int
		data   []byte
		base   int  // ofs-delta base offset, -1 when none
		baseID string
	}
	raws := make([]*rawEntry, 0, count)
	byOffset := map[int]*rawEntry{}
	off := 12
	readVarint := func() (size int, first byte, err error) {
		if off >= trailerStart {
			return 0, 0, errors.New("pack truncated")
		}
		b := pack[off]
		first = b
		size = int(b & 0x0f)
		shift := uint(4)
		off++
		for b&0x80 != 0 {
			if off >= trailerStart {
				return 0, 0, errors.New("pack truncated")
			}
			b = pack[off]
			off++
			size += int(b&0x7f) << shift
			shift += 7
		}
		return size, first, nil
	}
	for i := uint32(0); i < count; i++ {
		entryOff := off
		size, first, err := readVarint()
		if err != nil {
			return nil, err
		}
		code := (first >> 4) & 0x07
		e := &rawEntry{offset: entryOff, size: size, base: -1}
		switch code {
		case 1:
			e.typ = "commit"
		case 2:
			e.typ = "tree"
		case 3:
			e.typ = "blob"
		case 4:
			e.typ = "tag"
		case 6:
			if off >= trailerStart {
				return nil, errors.New("pack truncated")
			}
			b := pack[off]
			off++
			val := int(b & 0x7f)
			for b&0x80 != 0 {
				if off >= trailerStart {
					return nil, errors.New("pack truncated")
				}
				b = pack[off]
				off++
				val = ((val + 1) << 7) | int(b&0x7f)
			}
			e.base = entryOff - val
			e.typ = "ofs-delta"
		case 7:
			if off+20 > trailerStart {
				return nil, errors.New("pack truncated")
			}
			e.baseID = hex.EncodeToString(pack[off : off+20])
			off += 20
			e.typ = "ref-delta"
		default:
			return nil, errors.New("unknown pack entry type")
		}
		// inflate one zlib stream starting at off; measure its exact length
		// via the deflate stream (2-byte zlib header + deflate + 4-byte adler)
		zlen := zlibStreamLen(pack[off:trailerStart])
		if zlen <= 0 || zlen > trailerStart-off {
			return nil, errors.New("pack zlib stream bad")
		}
		zr, err := zlib.NewReader(bytes.NewReader(pack[off : off+zlen]))
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return nil, err
		}
		if len(data) != size {
			return nil, errors.New("pack entry size mismatch")
		}
		off += zlen
		e.data = data
		raws = append(raws, e)
		byOffset[entryOff] = e
	}
	if off != trailerStart {
		return nil, errors.New("pack trailing bytes")
	}
	resolved := map[int]*gitObject{}
	byOID := map[string]*gitObject{}
	var resolve func(e *rawEntry) (*gitObject, error)
	resolve = func(e *rawEntry) (*gitObject, error) {
		if o, ok := resolved[e.offset]; ok {
			return o, nil
		}
		var obj *gitObject
		switch e.typ {
		case "ofs-delta":
			base, ok := byOffset[e.base]
			if !ok {
				return nil, errors.New("ofs-delta base missing")
			}
			bobj, err := resolve(base)
			if err != nil {
				return nil, err
			}
			data, err := applyDelta(bobj.data, e.data)
			if err != nil {
				return nil, err
			}
			obj = &gitObject{typ: bobj.typ, data: data}
		case "ref-delta":
			if bobj, ok := byOID[e.baseID]; ok {
				data, err := applyDelta(bobj.data, e.data)
				if err != nil {
					return nil, err
				}
				obj = &gitObject{typ: bobj.typ, data: data}
			} else {
				return nil, errors.New("ref-delta base missing (thin pack)")
			}
		default:
			obj = &gitObject{typ: e.typ, data: e.data}
		}
		resolved[e.offset] = obj
		byOID[hashObject(obj.typ, obj.data)] = obj
		return obj, nil
	}
	var out []*gitObject
	seen := map[string]bool{}
	for _, e := range raws {
		obj, err := resolve(e)
		if err != nil {
			return nil, err
		}
		oid := hashObject(obj.typ, obj.data)
		if !seen[oid] {
			seen[oid] = true
			out = append(out, obj)
		}
	}
	return out, nil
}

func zlibStreamLen(buf []byte) int {
	if len(buf) < 2 {
		return 0
	}
	br := bytes.NewReader(buf[2:])
	fr := flate.NewReader(br)
	_, err := io.Copy(io.Discard, fr)
	fr.Close()
	if err != nil {
		return 0
	}
	deflateLen := len(buf[2:]) - br.Len()
	return 2 + deflateLen + 4
}

func applyDelta(base, delta []byte) ([]byte, error) {
	off := 0
	readVarint := func() (int, error) {
		v := 0
		shift := uint(0)
		for {
			if off >= len(delta) {
				return 0, errors.New("delta truncated")
			}
			b := delta[off]
			off++
			v |= int(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				break
			}
		}
		return v, nil
	}
	srcSize, err := readVarint()
	if err != nil {
		return nil, err
	}
	dstSize, err := readVarint()
	if err != nil {
		return nil, err
	}
	if srcSize != len(base) {
		return nil, errors.New("delta base size mismatch")
	}
	out := make([]byte, 0, dstSize)
	for off < len(delta) {
		op := delta[off]
		off++
		if op&0x80 != 0 {
			cpOff, cpSize := 0, 0
			for i := 0; i < 4; i++ {
				if op&(1<<uint(i)) != 0 {
					if off >= len(delta) {
						return nil, errors.New("delta truncated")
					}
					cpOff |= int(delta[off]) << (uint(i) * 8)
					off++
				}
			}
			for i := 0; i < 3; i++ {
				if op&(1<<uint(4+i)) != 0 {
					if off >= len(delta) {
						return nil, errors.New("delta truncated")
					}
					cpSize |= int(delta[off]) << (uint(i) * 8)
					off++
				}
			}
			if cpSize == 0 {
				cpSize = 0x10000
			}
			if cpOff+cpSize > len(base) {
				return nil, errors.New("delta copy out of range")
			}
			out = append(out, base[cpOff:cpOff+cpSize]...)
		} else if op != 0 {
			n := int(op & 0x7f)
			if off+n > len(delta) {
				return nil, errors.New("delta truncated")
			}
			out = append(out, delta[off:off+n]...)
			off += n
		} else {
			return nil, errors.New("delta opcode 0")
		}
	}
	if len(out) != dstSize {
		return nil, errors.New("delta size mismatch")
	}
	return out, nil
}

// ---------------------------------------------------------------- reachability

// reachableObjects returns every object reachable from tips (commits, tags,
// trees, blobs), following commit parents and tag targets.
func (r *repo) reachableObjects(tips []string) map[string]*gitObject {
	return r.reachableFrom(tips, true, nil)
}

// reachableFrom walks the object graph from tips. With followParents it
// descends into commit parents; commits listed in stopAt still contribute
// their trees and blobs but their parents are not followed (a shallow
// boundary the client already holds).
func (r *repo) reachableFrom(tips []string, followParents bool, stopAt map[string]bool) map[string]*gitObject {
	out := map[string]*gitObject{}
	queue := append([]string(nil), tips...)
	for len(queue) > 0 {
		oid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if _, ok := out[oid]; ok {
			continue
		}
		obj := r.objects[oid]
		if obj == nil {
			continue
		}
		out[oid] = obj
		switch obj.typ {
		case "commit":
			ci := r.getCommit(oid)
			if ci == nil {
				continue
			}
			queue = append(queue, ci.tree)
			if followParents && !stopAt[oid] {
				queue = append(queue, ci.parents...)
			}
		case "tag":
			if target := parseTag(obj.data); target != "" {
				queue = append(queue, target)
			}
		case "tree":
			for _, e := range r.getTree(oid) {
				if e.mode != "160000" {
					queue = append(queue, e.oid)
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- fetch selection

type fetchPlan struct {
	objects   []*gitObject // objects to send, deterministic order
	shallow   []string     // new shallow boundaries
	unshallow []string     // boundaries that are no longer shallow
}

// planFetch computes what a fetch sends.
// wants: tip oids; haves: oids the client holds; shallow: the client's
// current shallow boundaries; depth: 0 means no depth limit.
func (r *repo) planFetch(wants, haves, shallow []string, depth int) *fetchPlan {
	haveSet := map[string]bool{}
	for _, h := range haves {
		haveSet[h] = true
	}
	shallowSet := map[string]bool{}
	for _, s := range shallow {
		shallowSet[s] = true
	}
	// closure of everything the client already holds: follow parents, but
	// stop at the client's shallow boundaries (it holds the boundary
	// commit's tree, not its parents)
	haveObjs := map[string]bool{}
	if len(haves) > 0 {
		for oid := range r.reachableFrom(haves, true, shallowSet) {
			haveObjs[oid] = true
		}
	}
	// walk from wants; dist tracks generations from the nearest want
	type item struct {
		oid  string
		dist int
	}
	included := map[string]bool{} // commits/tags included
	boundary := map[string]bool{} // included commits whose parents are cut off
	seen := map[string]bool{}
	queue := []item{}
	for _, w := range wants {
		queue = append(queue, item{oid: w, dist: 0})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if seen[it.oid] {
			continue
		}
		seen[it.oid] = true
		obj := r.objects[it.oid]
		if obj == nil {
			continue
		}
		switch obj.typ {
		case "tag":
			included[it.oid] = true
			if target := parseTag(obj.data); target != "" {
				queue = append(queue, item{oid: target, dist: it.dist})
			}
		case "commit":
			if depth > 0 && it.dist >= depth {
				continue
			}
			included[it.oid] = true
			ci := r.getCommit(it.oid)
			if ci == nil {
				continue
			}
			cut := false
			if depth > 0 && it.dist == depth-1 {
				cut = len(ci.parents) > 0
			}
			if len(ci.parents) == 0 {
				// root commit: no boundary
			} else if depth == 0 && (haveSet[it.oid] || shallowSet[it.oid]) {
				// a have or an existing shallow boundary stops the walk,
				// but only when no depth limit is in play
				cut = false
			} else if depth == 0 {
				// keep walking unless the parent is held
			}
			if cut {
				boundary[it.oid] = true
				continue
			}
			for _, p := range ci.parents {
				if depth == 0 && (haveSet[p] || shallowSet[p]) {
					// parent is held/boundary: do not traverse into it, but
					// the current commit is not a boundary (its parent is
					// known to the client as held)
					continue
				}
				queue = append(queue, item{oid: p, dist: it.dist + 1})
			}
			// with a depth limit, a commit whose parents were not all
			// traversed because of the limit is a boundary; handled above
			if depth == 0 && len(ci.parents) > 0 {
				// not a boundary: parents are either held or queued
			}
		}
	}
	// objects to send: the included commits/tags and their trees/blobs only.
	// Parents are NOT followed here: the walk above already decided exactly
	// which commits are included (depth limits, haves, shallow boundaries).
	var tips []string
	for oid := range included {
		tips = append(tips, oid)
	}
	sort.Strings(tips)
	closure := r.reachableFrom(tips, false, nil)
	var objs []*gitObject
	var oids []string
	for oid := range closure {
		if !haveObjs[oid] {
			oids = append(oids, oid)
		}
	}
	sort.Strings(oids)
	for _, oid := range oids {
		objs = append(objs, closure[oid])
	}
	plan := &fetchPlan{objects: objs}
	if depth > 0 {
		for oid := range boundary {
			plan.shallow = append(plan.shallow, oid)
		}
		sort.Strings(plan.shallow)
		// unshallow: an old boundary whose parents are now included
		for _, s := range shallow {
			if !included[s] {
				continue
			}
			ci := r.getCommit(s)
			if ci == nil {
				continue
			}
			allIn := true
			for _, p := range ci.parents {
				if !included[p] {
					allIn = false
				}
			}
			if allIn && len(ci.parents) > 0 {
				plan.unshallow = append(plan.unshallow, s)
			}
		}
		sort.Strings(plan.unshallow)
	}
	return plan
}

// zlibStreamLen returns how many input bytes a zlib stream starting at buf[0]
// consumed. It re-inflates with compress/flate and measures the remainder.
