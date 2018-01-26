package main

import (
	"crypto/tls"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"math/rand"
	"strconv"
	"bytes"

	"github.com/goware/urlx"
	"github.com/valyala/fasthttp"
	"golang.org/x/net/http2"
)

func processUrl( url string ) string {

	var new_url string;
    new_url = string( url );

    url_len := len(new_url);
	
	for url_len > 0 {
	
		var buffer bytes.Buffer;
		
		idx := strings.Index( new_url, "@@RAND-" );
		if( idx != -1 ) {
	
			url_len := len(new_url);
		
			buffer.Write( []byte(new_url)[0:idx] );
			// fmt.Printf( "%s\n", buffer.String() );
			
			idx1 := idx + 7;
		
			str_num1 := string("");		
			for ; idx1 < url_len; idx1++ {
				if( new_url[idx1] == '-' ) {
					str_num1 = string( []byte(new_url)[idx+7:idx1] );
					// fmt.Printf( "%s\n", str_num1 );
					break;
				}
				if(( new_url[idx1] == '/' ) || ( new_url[idx1] == '/' ) ) {
					break;
				} 
			}
			
			if( str_num1 == "" ) {
				return string( url )
			}
		
			idx2 := idx1 + 1;
			str_num2 := string("");
			for ; idx2 < url_len; idx2++ {
				if( ( new_url[idx2] == '/' ) || ( new_url[idx2] == '.' ) ) {
					str_num2 = string( []byte(new_url)[idx1+1:idx2] );
					// fmt.Printf( "%s\n", str_num2 );
					break;
				}
				if( new_url[idx2] == '-' ) {
					break;
				} 
			}
			if( str_num2 == "" ) {
				return string( url );
			}
		
			num1, err1 := strconv.Atoi(str_num1);
			num2, err2 := strconv.Atoi(str_num2);
		
			if( err1 != nil ) {
				return string( url );
			}

			if( err2 != nil ) {
				return string( url );
			}
				
			num := rand.Intn( num2 - num1 );
		
			buffer.WriteString( strconv.Itoa( num1 + num ) );
			buffer.Write( []byte(new_url)[idx2:url_len]  );
			new_url = buffer.String();
			// fmt.Printf( "%s\n", new_url );		
		
		} else {
			break;
		}
	
	}
	
	return new_url;
}

type client interface {
	do() (code int, msTaken uint64, err error)
}

type bodyStreamProducer func() (io.ReadCloser, error)

type clientOpts struct {
	HTTP2 bool

	maxConns  uint64
	timeout   time.Duration
	tlsConfig *tls.Config

	headers     *headersList
	url, method string

	body    *string
	bodProd bodyStreamProducer

	bytesRead, bytesWritten *int64
}

type fasthttpClient struct {
	client *fasthttp.Client

	headers     *fasthttp.RequestHeader
	url, method string

	body    *string
	bodProd bodyStreamProducer
}

func newFastHTTPClient(opts *clientOpts) client {
	c := new(fasthttpClient)
	c.client = &fasthttp.Client{
		MaxConnsPerHost:               int(opts.maxConns),
		ReadTimeout:                   opts.timeout,
		WriteTimeout:                  opts.timeout,
		DisableHeaderNamesNormalizing: true,
		TLSConfig:                     opts.tlsConfig,
		Dial: fasthttpDialFunc(
			opts.bytesRead, opts.bytesWritten,
		),
	}
	c.headers = headersToFastHTTPHeaders(opts.headers)
	c.url, c.method, c.body = opts.url, opts.method, opts.body
	c.bodProd = opts.bodProd
	return client(c)
}

func (c *fasthttpClient) do() (
	code int, msTaken uint64, err error,
) {
	// prepare the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	if c.headers != nil {
		c.headers.CopyTo(&req.Header)
	}
	req.Header.SetMethod(c.method)
	
	url := processUrl( c.url ); 
	req.SetRequestURI( url)
	if c.body != nil {
		req.SetBodyString(*c.body)
	} else {
		bs, bserr := c.bodProd()
		if bserr != nil {
			return 0, 0, bserr
		}
		req.SetBodyStream(bs, -1)
	}

	// fire the request
	start := time.Now()
	err = c.client.Do(req, resp)
	if err != nil {
		code = -1
	} else {
		code = resp.StatusCode()
	}
	msTaken = uint64(time.Since(start).Nanoseconds() / 1000)

	// release resources
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	return
}

type httpClient struct {
	client *http.Client

	headers http.Header
	url     *url.URL
	url_raw string
	method  string

	body    *string
	bodProd bodyStreamProducer
}

func newHTTPClient(opts *clientOpts) client {
	c := new(httpClient)
	tr := &http.Transport{
		TLSClientConfig:     opts.tlsConfig,
		MaxIdleConnsPerHost: int(opts.maxConns),
	}
	tr.DialContext = httpDialContextFunc(opts.bytesRead, opts.bytesWritten)
	if opts.HTTP2 {
		_ = http2.ConfigureTransport(tr)
	} else {
		tr.TLSNextProto = make(
			map[string]func(authority string, c *tls.Conn) http.RoundTripper,
		)
	}

	cl := &http.Client{
		Transport: tr,
		Timeout:   opts.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	c.client = cl

	c.headers = headersToHTTPHeaders(opts.headers)
	c.method, c.body, c.bodProd = opts.method, opts.body, opts.bodProd
	var err error
	c.url, err = urlx.Parse(opts.url)
	c.url_raw = opts.url
	if err != nil {
		// opts.url guaranteed to be valid at this point
		panic(err)
	}

	return client(c)
}

func (c *httpClient) do() (
	code int, msTaken uint64, err error,
) {
	req := &http.Request{}

	req.Header = c.headers
	req.Method = c.method
	
	var url_err error
	req.URL, url_err = urlx.Parse( processUrl( c.url_raw ) );
	if url_err != nil {
		panic(err)
	}
	
	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
	}

	if c.body != nil {
		br := strings.NewReader(*c.body)
		req.Body = ioutil.NopCloser(br)
	} else {
		bs, bserr := c.bodProd()
		if bserr != nil {
			return 0, 0, bserr
		}
		req.Body = bs
	}

	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		code = -1
	} else {
		code = resp.StatusCode

		_, berr := io.Copy(ioutil.Discard, resp.Body)
		if berr != nil {
			err = berr
		}

		if cerr := resp.Body.Close(); cerr != nil {
			err = cerr
		}
	}
	msTaken = uint64(time.Since(start).Nanoseconds() / 1000)

	return
}

func headersToFastHTTPHeaders(h *headersList) *fasthttp.RequestHeader {
	if len(*h) == 0 {
		return nil
	}
	res := new(fasthttp.RequestHeader)
	for _, header := range *h {
		res.Set(header.key, header.value)
	}
	return res
}

func headersToHTTPHeaders(h *headersList) http.Header {
	if len(*h) == 0 {
		return http.Header{}
	}
	headers := http.Header{}

	for _, header := range *h {
		headers[header.key] = []string{header.value}
	}
	return headers
}
