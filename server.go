package main

// Omitted features:
//
// - Expect 100 Continue support
// - TLS
// - Most error checking
// - Only supports body that close, no persistent or chunked connections
// - Redirects

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/textproto"
	"strings"
)

type responseWriter struct {
	c *net.Conn
}

func (w responseWriter) Write(b []byte) (int, error) {
	log.Print("writing: " + string(b))
	return (*w.c).Write(b)
}

func logHandler(w responseWriter, r *request) {
	io.WriteString(w, "HTTP/1.0 200 OK\r\n")
	io.WriteString(w, "Content-Type: text/html; charset=utf-8\r\n")
	io.WriteString(w, "Content-Length: 14\r\n")
	io.WriteString(w, "\r\n")
	io.WriteString(w, "<h1>hello</h1>")
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as HTTP handlers. If f is a function
// with the appropriate signature, HandlerFunc(f) is a
// Handler that calls f.
type handlerFunc func(responseWriter, *request)

var defaultServeMux = make(map[string]handlerFunc)

func handle(pattern string, handler handlerFunc) {
	defaultServeMux[pattern] = handler
}

func findHandler(r *request) (handlerFunc, error) {
	for k, v := range defaultServeMux {
		// Bunch of ignored bugs here.
		//   - duplicate paths
		//   - prefix and slash semantics
		//   - matching longest prefix
		if strings.HasPrefix(r.uri, k) {
			log.Printf("Found handler %s that matched uri: %s", k, r.uri)
			return v, nil
		}
	}
	return nil, errors.New("no handler for path: " + r.uri)
}

func notFoundHandler(w responseWriter, r *request) {
	const errorHeaders = "\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n"
	_, _ = w.Write([]byte("HTTP/1.0 404 Not Found" + errorHeaders))
}

type header map[string][]string

type request struct {
	method string
	header header
	body   []byte
	uri    string
	proto  string // "HTTP/1.1"
	ctx    context.Context
}

func readRequest(c *net.Conn) (*request, error) {
	b := bufio.NewReader(*c)
	tp := textproto.NewReader(b)
	req := new(request)

	// First line: parse "GET /index.html HTTP/1.0"
	var s string
	s, _ = tp.ReadLine()
	s1 := strings.Index(s, " ")
	s2 := strings.Index(s[s1+1:], " ")
	s2 += s1 + 1
	req.method, req.uri, req.proto = s[:s1], s[s1+1 : s2], s[s2+1:]
	log.Printf("parsed request: method=%s, uri=%s, proto=%s", req.method, req.uri, req.proto)

	// Parse headers
	mimeHeader, _ := tp.ReadMIMEHeader()
	req.header = header(mimeHeader)
	log.Printf("request headers:")
	for k, vs := range mimeHeader {
		for _, v := range vs {
			log.Printf("    %s: %s", k, v)
		}
	}

	if req.method == "GET" {
		// No body for GET
		return req, nil
	}

	body := make([]byte, 1024)
	n, _ := b.Read(body)
	// Rest is the body
	req.body = body[:n]
	log.Printf("body: " + string(req.body))
	return req, nil
}

func main() {

	handle("/", handlerFunc(logHandler))
	handle("/notfound", handlerFunc(notFoundHandler))

	addr := "127.0.0.1:8080"
	ln, _ := net.Listen("tcp", addr)
	ln = ln.(*net.TCPListener)

	log.Print("===============")
	log.Print("Server Started!")
	log.Print("===============")
	log.Print("")
	log.Print("addr: http://" + addr)

	for {
		// Initialize connection
		rw, e := ln.Accept()
		c := &rw
		log.Print()
		log.Print()
		log.Print("incoming connection")
		if e != nil {
			panic(e)
		}

		// Read request
		log.Print()
		log.Print("Reading request")
		req, err := readRequest(c)
		if err != nil {
			panic(err)
		}

		// Write response
		log.Print()
		log.Print("Writing response")
		h, err := findHandler(req)
		if err != nil {
			log.Fatal(err.Error())
			continue
		}
		w := responseWriter{c}
		h(w, req)
	}
}
