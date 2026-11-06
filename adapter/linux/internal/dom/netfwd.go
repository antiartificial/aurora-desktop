package dom

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Inside a domain the network namespace is empty: loopback and nothing else.
// Most programs only know proxies as host:port, so the domain's one TCP
// listener is this relay on 127.0.0.1, which does nothing but copy bytes to the
// unix socket the host's NetProxy listens on. It decides nothing; every
// decision is made on the host side of that socket, where the domain cannot
// reach the code or the policy.
const (
	NetFwdCmd  = "__netfwd" // argv[1] of the helper; handled by RunNetFwd
	NetFwdAddr = "127.0.0.1:3128"
	NetSock    = "/run/aurora/net.sock"
	NetFwdPath = "/run/aurora-netfwd"
)

// RunNetFwd is the helper's main, run INSIDE the domain as the payload's
// parent: `<exe> __netfwd -- <payload argv...>`. It relays NetFwdAddr to
// NetSock, runs the payload with its stdio and environment, and exits with the
// payload's status.
func RunNetFwd(args []string) int {
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(os.Stderr, "netfwd: usage: __netfwd -- <command...>")
		return 125
	}
	ln, err := net.Listen("tcp", NetFwdAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "netfwd: cannot listen on %s inside the domain: %v\n", NetFwdAddr, err)
		return 125
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go relay(c)
		}
	}()
	cmd := exec.Command(args[1], args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "netfwd: %v\n", err)
		return 127
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range sig {
			cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	if err != nil {
		return 126
	}
	return 0
}

func relay(c net.Conn) {
	defer c.Close()
	up, err := net.Dial("unix", NetSock)
	if err != nil {
		fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); up.(*net.UnixConn).CloseWrite(); done <- struct{}{} }()
	go func() { io.Copy(c, up); c.(*net.TCPConn).CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
}
