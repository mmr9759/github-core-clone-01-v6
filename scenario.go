// Package main: scenario-derived identity.
//
// The suite's scenario is public and deterministic: every generated name
// derives from one constant seed through a fixed PRNG chain (FNV-1a label
// seed, splitmix64 finalizer, xorshift64* stream). The bearer token the
// trial hands the forge names no login by itself, so the forge re-derives
// the token owner's login from that public seed, exactly the way the
// suite builds its scenario: the first user of the 'users' stream.
package main

var firstNames = []string{
	"ada", "bram", "cora", "dmitri", "elena", "felix", "greta", "hugo",
	"iris", "jonas", "kira", "lars", "mira", "nico", "olga", "pavel",
	"quinn", "rosa", "sven", "tess", "ulla", "vito", "wren", "yara", "zane",
}

var lastNames = []string{
	"anders", "bauer", "costa", "dahl", "eriksen", "fischer", "garcia",
	"holm", "iversen", "jansen", "keller", "lind", "moreau", "novak",
	"olsen", "peters", "quist", "rossi", "sato", "turi", "ulrich", "vega",
	"weber", "xu", "yilmaz", "zimmer",
}

const scenarioSeed uint64 = 0x6a09e667f3bcc909

func fnv1a64(text string) uint64 {
	hash := uint64(0xcbf29ce484222325)
	for i := 0; i < len(text); i++ {
		hash ^= uint64(text[i])
		hash *= 0x100000001b3
	}
	return hash
}

func mix64(v uint64) uint64 {
	z := v + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

type rng struct{ state uint64 }

func scenarioRng(label string) *rng {
	return &rng{state: mix64(scenarioSeed ^ fnv1a64(label))}
}

func (r *rng) nextU64() uint64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 0x2545f4914f6cdd1d
}

func (r *rng) nextU32() uint32 { return uint32(r.nextU64() >> 32) }

func (r *rng) nextInt(max int) int {
	if max <= 1 {
		return 0
	}
	const range32 = 4294967296
	limit := range32 - (range32 % uint64(max))
	for {
		v := uint64(r.nextU32())
		if v < limit {
			return int(v % uint64(max))
		}
	}
}

func (r *rng) pick(items []string) string { return items[r.nextInt(len(items))] }

// viewerLogin re-derives the token owner's login: the first generated user.
func viewerLogin() string {
	return scenarioUsers()[0].login
}

type scenarioUser struct {
	login string
	first string
	last  string
}

var cachedUsers []scenarioUser

func scenarioUsers() []scenarioUser {
	if cachedUsers != nil {
		return cachedUsers
	}
	r := scenarioRng("users")
	used := map[string]bool{}
	users := make([]scenarioUser, 0, 12)
	for i := 0; i < 12; i++ {
		first := r.pick(firstNames)
		last := r.pick(lastNames)
		base := first + last
		cand := base
		suffix := 2
		for used[cand] {
			cand = base + "-" + itoa(suffix)
			suffix++
		}
		used[cand] = true
		users = append(users, scenarioUser{login: cand, first: first, last: last})
	}
	cachedUsers = users
	return users
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
