package main

// Simple, single-threaded server using system calls instead of the net library.
//
// Omitted features from the go net package:
//
// - TLS
// - Most error checking
// - Only supports bodies that close, no persistent or chunked connections
// - Redirects
// - Deadlines and cancellation
// - Non-blocking sockets

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/textproto"
	"os"
	"strings"
	"syscall"
)

// netSocket is a file descriptor for a system socket.
type netSocket struct {
	// System file descriptor.
	fd int
}

func (ns netSocket) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := syscall.Read(ns.fd, p)
	if err != nil {
		n = 0
	}
	return n, err
}

func (ns netSocket) Write(p []byte) (int, error) {
	n, err := syscall.Write(ns.fd, p)
	if err != nil {
		n = 0
	}
	return n, err
}

// Creates a new netSocket for the next pending connection request.
func (ns *netSocket) Accept() (*netSocket, error) {
	// syscall.ForkLock doc states lock not needed for blocking accept.
	nfd, _, err := syscall.Accept(ns.fd)
	if err == nil {
		syscall.CloseOnExec(nfd)
	}
	if err != nil {
		return nil, err
	}
	return &netSocket{nfd}, nil
}

func (ns *netSocket) Close() error {
	return syscall.Close(ns.fd)
}

// Creates a new socket file descriptor, binds it and listens on it.
func newNetSocket(ip net.IP, port int) (*netSocket, error) {
	// ForkLock docs state that socket syscall requires the lock.
	syscall.ForkLock.Lock()
	// AF_INET = Address Family for IPv4
	// SOCK_STREAM = virtual circuit service
	// 0: the protocol for SOCK_STREAM, there's only 1.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	syscall.ForkLock.Unlock()

	// Allow reuse of recently-used addresses.
	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fd)
		return nil, os.NewSyscallError("setsockopt", err)
	}

	sa := &syscall.SockaddrInet4{Port: port}
	copy(sa.Addr[:], ip)

	// Bind the socket to a port
	if err = syscall.Bind(fd, sa); err != nil {
		return nil, os.NewSyscallError("bind", err)
	}

	// Listen for incoming connections.
	if err = syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		return nil, os.NewSyscallError("listen", err)
	}

	return &netSocket{fd: fd}, nil
}

// Facade in front of netSocket for nicer types and to log writes.
type responseWriter struct {
	ns *netSocket
}

func (w responseWriter) Write(b []byte) (int, error) {
	log.Print("writing: " + string(b))
	return (*w.ns).Write(b)
}

// Type adapter to allow use of ordinary functions as handlers.
type handlerFunc func(responseWriter, *request) error

type serveMux map[string]handlerFunc

var muxes = make(serveMux)

func (m serveMux) handle(pattern string, handler handlerFunc) {
	m[pattern] = handler
}

// Finds the a handler that matches the request path.
// Picks the longest handler in case of a tie.
func (m serveMux) findHandler(r *request) (handlerFunc, error) {
	var h handlerFunc = nil
	var l = 0
	for k, v := range m {
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

// Writes the response using the handler that best matches the request.
func (m serveMux) dispatch(w responseWriter, r *request) error {
	h, err := m.findHandler(r)
	if err != nil {
		return err
	}
	return h(w, r)
}

func writeHtml(f func(*request) string) handlerFunc {
	return func(w responseWriter, r *request) error {
		html := f(r)
		io.WriteString(w, "HTTP/1.0 200 OK\r\n")
		io.WriteString(w, "Content-Type: text/html; charset=utf-8\r\n")
		fmt.Fprintf(w, "Content-Length: %d\r\n", len(html))
		io.WriteString(w, "\r\n")
		io.WriteString(w, html)
		return nil
	}
}

func notFound(w responseWriter, r *request) error {
	_, err := io.WriteString(w, "HTTP/1.0 404 Not Found\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"Content-Length: 0\r\n"+
		"Connection: close\r\n"+
		"\r\n")
	return err
}

type request struct {
	method string // GET, POST, etc.
	header textproto.MIMEHeader
	body   []byte
	uri    string // The raw URI from the request
	proto  string // "HTTP/1.1"
}

func parseRequest(c *netSocket) (*request, error) {
	b := bufio.NewReader(*c)
	tp := textproto.NewReader(b)
	req := new(request)

	// First line: parse "GET /index.html HTTP/1.0"
	var s string
	s, _ = tp.ReadLine()
	sp := strings.Split(s, " ")
	req.method, req.uri, req.proto = sp[0], sp[1], sp[2]

	// Parse headers
	mimeHeader, _ := tp.ReadMIMEHeader()
	req.header = mimeHeader

	// Parse body
	if req.method == "GET" || req.method == "HEAD" {
		return req, nil
	}
	body, err := ioutil.ReadAll(b)
	if err != nil {
		return nil, err
	}
	req.body = body
	return req, nil
}

func main() {
	ipFlag := flag.String("ip_addr", "127.0.0.1", "The IP address to use")
	portFlag := flag.Int("port", 8080, "The port to use.")
	flag.Parse()

	muxes.handle("/hello",
		writeHtml(func(_ *request) string { return "<h1>Hello world</h1>" }))
	muxes.handle("/notfound", handlerFunc(notFound))
	muxes.handle("/",
		writeHtml(func(r *request) string {
			return "<h1>Using fallback matcher for path: " + r.uri + "</h1>"
		}))

	ip := net.ParseIP(*ipFlag)
	port := *portFlag
	socket, err := newNetSocket(ip, port)
	defer socket.Close()
	if err != nil {
		panic(err)
	}

	log.Print("===============")
	log.Print("Server Started!")
	log.Print("===============")
	log.Print("")
	log.Printf("addr: http://%s:%d", ip, port)

	for {
		// Block until incoming connection
		rw, e := socket.Accept()
		log.Print()
		log.Print()
		log.Printf("Incoming connection")
		if e != nil {
			panic(e)
		}

		// Read request
		log.Print("Reading request")
		req, err := parseRequest(rw)
		log.Print("request: ", req)
		if err != nil {
			panic(err)
		}

		// Write response
		log.Print("Writing response")
		err = muxes.dispatch(responseWriter{rw}, req)
		if err != nil {
			log.Print(err.Error())
			continue
		}
	}
}
