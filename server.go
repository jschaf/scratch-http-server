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
	"os"
	"strings"
	"syscall"
)

// netFD is a file descriptor.
type netFD struct {
	// System file descriptor.
	Sysfd int
}

func (fd *netFD) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := syscall.Read(fd.Sysfd, p)
	if err != nil {
		n = 0
	}
	return n, err
}

func (fd *netFD) Write(p []byte) (int, error) {
	n, err := syscall.Write(fd.Sysfd, p)
	if err != nil {
		n = 0
	}
	return n, err
}

// Implement Read and Write

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

func notFoundHandler(w responseWriter, r *request) {
	io.WriteString(w, "HTTP/1.0 404 Not Found\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"Content-Length: 0\r\n"+
		"Connection: close\r\n"+
		"\r\n")
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
	var h handlerFunc = nil
	var l = 0
	for k, v := range defaultServeMux {
		// Bunch of ignored bugs here.
		//   - duplicate paths
		//   - prefix and slash semantics
		//   - matching longest prefix
		if strings.HasPrefix(r.uri, k) {
			log.Printf("Found handler %s that matched uri: %s", k, r.uri)

			if len(k) > l {
				l = len(k)
				h = v
			}
		}
	}
	if h == nil {
		return nil, errors.New("no handler for path: " + r.uri)
	}
	return h, nil
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

	if req.method == "GET" || req.method == "HEAD" {
		return req, nil
	}

	body := make([]byte, 1024)
	n, _ := b.Read(body)
	// Rest is the body
	req.body = body[:n]
	log.Printf("body: " + string(req.body))
	return req, nil
}

func newNetFD(ip net.IP, port int) (*netFD, error) {
	syscall.ForkLock.Lock()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}

	if err = syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, os.NewSyscallError("setnonblock", err)
	}

	// Allow broadcast
	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
		syscall.Close(fd)
		return nil, os.NewSyscallError("setsockopt", err)
	}
	syscall.ForkLock.Unlock()

	// Allow reuse of recently-used addresses.
	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fd)
		return nil, os.NewSyscallError("setsockopt", err)
	}

	sa := &syscall.SockaddrInet4{Port: port}
	copy(sa.Addr[:], ip)

	if err = syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		return nil, os.NewSyscallError("listen", err)
	}

	return &netFD{Sysfd: fd}, nil
}

func main() {

	handle("/", handlerFunc(logHandler))
	handle("/notfound", handlerFunc(notFoundHandler))

	host, port := "127.0.0.1", 8080

	// socket returns a network file descriptor that is ready for
	// asynchronous I/O using the network poller.
	syscall.ForkLock.Lock()
	// SYS_SOCKET(domain=2, typ=1, proto=0)
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		panic(os.NewSyscallError("socket", err))
	}
	if err = syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		panic(os.NewSyscallError("setnonblock", err))
	}

	// Allow broadcast
	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
		syscall.Close(fd)
		panic(os.NewSyscallError("setsockopt", err))
	}
	syscall.ForkLock.Unlock()

	// Allow reuse of recently-used addresses.
	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fd)
		panic(os.NewSyscallError("setsockopt", err))
	}

	ip := net.IPv4(127, 0, 0, 1)
	sa := &syscall.SockaddrInet4{Port: port}
	copy(sa.Addr[:], ip)

	if err = syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		panic(os.NewSyscallError("listen", err))
	}

	lsa, _ := syscall.Getsockname(fd)

	//ln, _ := net.Listen("tcp", addr)
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
