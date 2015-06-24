/*
   Copyright 2015 Albus <albus@shaheng.me>.
   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	headerRegexp = "^([\\w-]+):\\s*(.+)"
)

var CipherSuites = map[string]uint16{
	"TLS_RSA_WITH_RC4_128_SHA":                uint16(0x0005),
	"TLS_RSA_WITH_3DES_EDE_CBC_SHA":           uint16(0x000a),
	"TLS_RSA_WITH_AES_128_CBC_SHA":            uint16(0x002f),
	"TLS_RSA_WITH_AES_256_CBC_SHA":            uint16(0x0035),
	"TLS_ECDHE_ECDSA_WITH_RC4_128_SHA":        uint16(0xc007),
	"TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA":    uint16(0xc009),
	"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA":    uint16(0xc00a),
	"TLS_ECDHE_RSA_WITH_RC4_128_SHA":          uint16(0xc011),
	"TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA":     uint16(0xc012),
	"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA":      uint16(0xc013),
	"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA":      uint16(0xc014),
	"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256":   uint16(0xc02f),
	"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256": uint16(0xc02b),

	// TLS_FALLBACK_SCSV isn't a standard cipher suite but an indicator
	// that the client is doing version fallback. See
	// https://tools.ietf.org/html/draft-ietf-tls-downgrade-scsv-00.
	"TLS_FALLBACK_SCSV": uint16(0x5600),
}

var (
	help        *bool   = flag.Bool("h", false, "show help")
	url         *string = flag.String("u", "https://0.0.0.0:28080/", "server url")
	concurrency *int    = flag.Int("c", 1, "concurrency:the worker's number,1 default")
	reqNum      *int    = flag.Int("r", 0, "total requests per connection,0 default")
	dur         *int    = flag.Int("t", 0, "timelimit (msec),0 default")
	keepAlive   *bool   = flag.Bool("k", false, "keep the connections every worker established alive,false default")
	withReq     *bool   = flag.Bool("w", false, "send request after handshake connection,false default")
	cipherSuite *string = flag.String("s", "TLS_RSA_WITH_RC4_128_SHA", "cipher suite,TLS_RSA_WITH_RC4_128_SHA default")
	method      *string = flag.String("m", "GET", "HTTP Method,GET default")
	headers     *string = flag.String("H", "", "request Headers,empty default")
	body        *string = flag.String("B", "", "request Body,empty default")
	out         *bool   = flag.Bool("o", false, "print response body")
)

var (
	proto        string
	host         string
	port         string
	address      string
	path         string
	swithHttp    bool            = false
	network      string          = "tcp"
	servers      map[string]bool = make(map[string]bool)
	header       http.Header     = make(http.Header)
	cipherSuites []uint16
)

type Reporter struct {
	Server              string
	Hostname            string
	Port                string
	Path                string
	Headers             string
	ContentLength       int64
	Concurrency         int
	TimeTaken           int64
	TimeDur             int64
	TotalRequest        int
	FailedRequest       int
	RequestPerSecond    int
	ConnectionPerSecond int
	Non2XXCode          int
}

func (r *Reporter) Printer() error {
	report := fmt.Sprintf("Server Software:%s\nServer Hostname:%s\nServer Port:%s\n\nRequest Headers:\n%s\n\nDocument Path:%s\nDocument Length:%d\n\nConcurrency:%d\nTime Duration:%dms\nAvg Time Taken:%dms\n\nComplete Requests:%d\nFailed Request:%d\n\nRequest Per Second:%d\nConnections Per Second:%d\n\nNon2XXCode:%d\n\n", r.Server, r.Hostname, r.Port, r.Headers, r.Path, r.ContentLength, r.Concurrency, r.TimeDur, r.TimeTaken/1000/int64(r.TotalRequest), r.TotalRequest, r.FailedRequest, r.RequestPerSecond, r.ConnectionPerSecond, r.Non2XXCode)
	fmt.Println(report)
	return nil
}

func printHelp() {
	fmt.Println("Usage: iBenchmark [options]")
	flag.PrintDefaults()
	fmt.Printf("\ncihper suite:\n")
	for k := range CipherSuites {
		fmt.Printf("  %s\n", k)
	}
	os.Exit(1)
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			fmt.Println(err)
			printHelp()
		}
	}()
	flag.Parse()
	if *help {
		printHelp()
	}
	//http https support only
	proto = (*url)[:strings.Index(*url, ":")]
	if proto == "http" {
		swithHttp = true
	} else {
		if proto != "https" {
			printHelp()
		}
	}
	host = (*url)[strings.Index(*url, "//")+2 : strings.LastIndexAny(*url, ":")]
	port = (*url)[strings.LastIndex(*url, ":")+1 : strings.LastIndex(*url, "/")]
	address = host + ":" + port
	path = (*url)[strings.LastIndex(*url, "/"):]
	if *headers != "" {
		headers := strings.Split(*headers, ";")
		for _, h := range headers {
			match, err := parseHeader(h, headerRegexp)
			if err != nil {
				fmt.Println(err)
				printHelp()
			}
			header.Set(match[1], match[2])
		}
	}
	if host == "" || port == "" || path == "" || proto == "" {
		printHelp()
	}
	ciphers := strings.Split(*cipherSuite, ",")
	for _, c := range ciphers {
		cipherSuites = append(cipherSuites, CipherSuites[c])
	}

	runtime.GOMAXPROCS(8)

	timeout := time.Duration(*dur) * time.Millisecond
	finChan := make([]chan bool, *concurrency)

	// number of connections to crypto server cluster
	reporter := new(Reporter)
	reporter.Concurrency = *concurrency
	reporter.Hostname = host
	reporter.Port = port
	reporter.Path = path

	fmt.Println("benchmark start ")
	// start workers
	start := time.Now()
	for i := 0; i < *concurrency; i = i + 1 {
		finChan[i] = make(chan bool)
		go worker(*reqNum, timeout, reporter, finChan[i])
	}

	// wait for finish
	for i := 0; i < *concurrency; i = i + 1 {
		switch {
		case <-(finChan[i]):
			continue
		}
	}
	duration := time.Since(start).Nanoseconds() / (1000 * 1000)
	reporter.TimeDur = duration
	if *keepAlive {
		reporter.RequestPerSecond = int(float64(reporter.TotalRequest) / (float64(reporter.TimeDur) / 1000))
		reporter.ConnectionPerSecond = 0
	} else {
		reporter.ConnectionPerSecond = int(float64(reporter.TotalRequest) / (float64(reporter.TimeDur) / 1000))
		reporter.RequestPerSecond = 0
	}
	var server string
	for key, _ := range servers {
		server = fmt.Sprintf("%s %s", server, key)
	}
	//generate header info
	reporter.Server = server
	for k, v := range header {
		var val string
		for _, v := range v {
			val += v + " "
		}
		reporter.Headers += k + ":" + val + "\r\n"
	}
	time.Sleep(1 * time.Second)
	reporter.Printer()
}

//parse headers:'header1:v1;header2:v2'
func parseHeader(in, reg string) (matches []string, err error) {
	re := regexp.MustCompile(reg)
	matches = re.FindStringSubmatch(in)
	if len(matches) < 1 {
		err = errors.New(fmt.Sprintf("Could not parse provided input:%s", err.Error()))
	}
	return
}

//establish a transport connection,and send queries if withReq on the connection
//and the queries depend on the param dur or requests.if both were setted,depend on dur.See worker func.
//otherwise close the connection immediately when established.
func (r *Reporter) GetResponse(conn *net.Conn) error {
	var resp *http.Response
	var err error
	procStart := time.Now()
	r.TotalRequest += 1
	if !swithHttp {
		if !*keepAlive {
			resp, err = HTTPSGet()
		} else {
			resp, err = HTTPSGet_KeepAlive(conn)
		}

	} else {
		if !*keepAlive {
			resp, err = HTTPGet()
		} else {
			resp, err = HTTPGet_KeepAlive(conn)
		}
	}
	if err != nil {
		fmt.Println(fmt.Sprintf("HTTP(S) GET ERROR %v", err))
		r.FailedRequest += 1
	}
	if resp != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			r.Non2XXCode += 1
		}
		r.ContentLength = resp.ContentLength
		for _, server := range resp.Header["Server"] {
			if !servers[server] {
				servers[server] = true
			}
		}
		if err := resp.Body.Close(); err != nil {
			return err
		}
	}
	end := time.Now()
	elapse := end.Sub(procStart).Nanoseconds() / 1000
	r.TimeTaken += elapse
	return err
}

//init a go routine,send queries on the transport layer ,the queries number depend on the reqNum or timeout.
//And if both were setted,depends on timeout.
//the finChan notify the main process wether this go routine has finished
func worker(reqNum int, timeout time.Duration, reporter *Reporter, finChan chan bool) {
	end_time := time.After(timeout)
	var conn net.Conn

	defer func() {
		if conn != nil {
			conn.Close()
		}

	}()
	if *dur != 0 {
		for {
			select {
			case <-end_time:
				finChan <- true
				return
			default:
				err := reporter.GetResponse(&conn)
				if err != nil {
					fmt.Println(fmt.Sprintf("[ERROR]:%s", err))
					if conn != nil {
						conn.Close()
						conn = nil
					}
				}

			}
		}

	} else {
		for i := 0; i < reqNum; i++ {
			err := reporter.GetResponse(&conn)
			if err != nil {
				fmt.Println(fmt.Sprintf("[ERROR]:%s", err))
				if conn != nil {
					conn.Close()
					conn = nil
				}
			}
		}
		finChan <- true
		return
	}

}

//establish a new tls connection and send send query if withReq
func HTTPSGet() (*http.Response, error) {
	// create tls config
	config := tls.Config{
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
		CipherSuites:           cipherSuites,
	}
	// connect to tls server
	conn, err := tls.Dial(network, address, &config)
	if err != nil {
		fmt.Errorf("client: dial: %s", err)
		return nil, err
	}
	if *withReq {
		return SendQuery(conn)
	} else {
		return nil, nil
	}
}

//establish a new tls connection first time,and later reuse the connection,send query if withReq
func HTTPSGet_KeepAlive(conn *net.Conn) (*http.Response, error) {
	// create tls config
	config := tls.Config{
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
		CipherSuites:           cipherSuites,
	}
	var err error
	// connect to tls server
	if *conn == nil {
		*conn, err = tls.Dial(network, address, &config)
		if err != nil {
			fmt.Errorf("client: dial: %s", err)
			return nil, err
		}

	}
	if *withReq {
		return SendQuery(*conn)
	} else {
		return nil, nil
	}
}

//establish a new tcp connection and send query if withReq
func HTTPGet() (*http.Response, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}

	if *withReq {
		return SendQuery(conn)
	} else {
		return nil, nil
	}
}

//establish a new tcp connection first time,and later reuse the connection,send query if withReq
func HTTPGet_KeepAlive(conn *net.Conn) (*http.Response, error) {
	var err error
	if *conn == nil {
		*conn, err = net.Dial(network, address)
		if err != nil {
			return nil, err
		}

	}
	if *withReq {
		return SendQuery(*conn)
	} else {
		return nil, nil
	}
}

//send query on the established connection,and get the response
func SendQuery(conn net.Conn) (*http.Response, error) {
	if conn == nil {
		return nil, errors.New("send queries on the nil or closed connection")
	}
	req, err := http.NewRequest(*method, *url, strings.NewReader(*body))
	if err != nil {
		return nil, err
	}
	req.Header = header
	if header.Get("Host") != "" {
		//I think this should be a golang http pkg's bug.
		//if I put Host Header in the req.Header,golang pkg can't handle it.
		//So I have to hanlde the Host header in my code.
		req.Host = header.Get("Host")
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, err
	}
	if *out {
		var bout bytes.Buffer
		io.Copy(&bout, resp.Body)
		if bout.String() != "" {
			fmt.Println(bout.String())
		}
	}
	return resp, nil
}
