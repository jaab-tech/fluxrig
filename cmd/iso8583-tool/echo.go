package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
)

var (
	port = flag.String("port", "8590", "Port to listen on (Echo Mode)")
	sink = flag.Bool("sink", false, "Sink mode (discard data instead of echo)")
)

func runEchoMode() {
	// Log to stdout
	log.SetOutput(os.Stdout)

	addr := ":" + *port

	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Error listening: %v", err)
	}
	defer func() { _ = l.Close() }()

	log.Printf("TCP Echo Server listening on %s (Sink: %v)", addr, *sink)

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("Error accepting: %v", err)
			continue
		}
		go handleRequest(conn, *sink)
	}
}

func handleRequest(conn net.Conn, sink bool) {
	defer func() { _ = conn.Close() }()
	if sink {
		// Discard everything (Blackhole)
		_, _ = io.Copy(io.Discard, conn)
	} else {
		// Echo everything
		_, _ = io.Copy(conn, conn)
	}
}
