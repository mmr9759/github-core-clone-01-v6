package main

import (
	"net"
	"os"
	"strings"
	"time"
)

var viewer string
var viewerName string
var viewerEmail string
var shopToken string

func main() {
	viewer = viewerLogin()
	u := scenarioUsers()[0]
	viewerName = u.first + " " + u.last
	viewerEmail = viewer + "@example.invalid"
	shopToken = os.Getenv("MAKEFUN_SHOP_TOKEN")

	st := newStore()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		panic(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, st)
		}
	}()

	envURL := os.Getenv("MAKEFUN_ENV_URL")
	if envURL == "" {
		// no trial environment: serve forever (used by local smoke tests)
		select {}
	}
	// register this forge with the trial environment
	registerBody := `{"port":` + itoa(port) + `}`
	for attempt := 0; attempt < 30; attempt++ {
		_, status, err := envRequest("POST", strings.TrimSuffix(envURL, "/")+"/register", registerBody)
		if err == nil && status < 300 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// poll until the trial is done
	delay := 250 * time.Millisecond
	for {
		body, _, err := envRequest("GET", strings.TrimSuffix(envURL, "/")+"/done", "")
		if err == nil && strings.Contains(string(body), `"done":true`) {
			os.Exit(0)
		}
		time.Sleep(delay)
		if delay < 2*time.Second {
			delay *= 2
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
		}
	}
}
