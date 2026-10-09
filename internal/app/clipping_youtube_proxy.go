package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	clippingYouTubeProxyMaxTunnels  = 12
	clippingYouTubeProxyDialTimeout = 10 * time.Second
	clippingYouTubeProxyHeaderTime  = 8 * time.Second
)

type youtubeProxyDial func(context.Context, string, string) (net.Conn, error)

type youtubeProxy struct {
	listener net.Listener
	server   *http.Server
	resolver clippingIPResolver
	dial     youtubeProxyDial
	username string
	password string
	address  string

	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	closed    chan struct{}
	closeOnce sync.Once
	serveErr  error
	tunnels   chan struct{}
}

func startYouTubeProxy(ctx context.Context, resolver clippingIPResolver) (*youtubeProxy, error) {
	if resolver == nil {
		return nil, errors.New("YouTube proxy resolver is unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("YouTube proxy listener could not start")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		_ = listener.Close()
		return nil, errors.New("YouTube proxy authentication could not be initialized")
	}
	proxy := &youtubeProxy{
		listener: listener,
		resolver: resolver,
		username: "framevault",
		password: base64.RawURLEncoding.EncodeToString(secret),
		address:  listener.Addr().String(),
		conns:    make(map[net.Conn]struct{}),
		closed:   make(chan struct{}),
		tunnels:  make(chan struct{}, clippingYouTubeProxyMaxTunnels),
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: clippingYouTubeProxyDialTimeout, KeepAlive: 20 * time.Second}
			return dialer.DialContext(ctx, network, address)
		},
	}
	proxy.server = &http.Server{
		Handler:           http.HandlerFunc(proxy.handleConnect),
		ReadHeaderTimeout: clippingYouTubeProxyHeaderTime,
		IdleTimeout:       10 * time.Second,
		MaxHeaderBytes:    4 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		err := proxy.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			proxy.mu.Lock()
			proxy.serveErr = errors.New("YouTube proxy stopped unexpectedly")
			proxy.mu.Unlock()
		}
		proxy.Close()
	}()
	go func() {
		select {
		case <-ctx.Done():
			proxy.Close()
		case <-proxy.closed:
		}
	}()
	return proxy, nil
}

func (proxy *youtubeProxy) AuthenticatedURL() string {
	if proxy == nil {
		return ""
	}
	return (&url.URL{Scheme: "http", Host: proxy.address, User: url.UserPassword(proxy.username, proxy.password)}).String()
}

func (proxy *youtubeProxy) Err() error {
	if proxy == nil {
		return errors.New("YouTube proxy is unavailable")
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return proxy.serveErr
}

func (proxy *youtubeProxy) Close() {
	if proxy == nil {
		return
	}
	proxy.closeOnce.Do(func() {
		close(proxy.closed)
		_ = proxy.server.Close()
		_ = proxy.listener.Close()
		proxy.mu.Lock()
		for conn := range proxy.conns {
			_ = conn.Close()
		}
		proxy.mu.Unlock()
	})
}

func (proxy *youtubeProxy) handleConnect(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodConnect || request.URL == nil || request.URL.Host == "" {
		http.Error(writer, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	if !proxy.authenticated(request.Header.Get("Proxy-Authorization")) {
		writer.Header().Set("Proxy-Authenticate", `Basic realm="framevault-youtube-import"`)
		http.Error(writer, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if !strings.EqualFold(request.Host, request.URL.Host) {
		http.Error(writer, "invalid CONNECT authority", http.StatusBadRequest)
		return
	}
	host, port, err := net.SplitHostPort(request.URL.Host)
	if err != nil || port != "443" || strings.HasSuffix(host, ".") {
		http.Error(writer, "destination is not approved", http.StatusForbidden)
		return
	}
	host = strings.ToLower(host)
	if !asciiDNSName(host) || !isYouTubeProxyHost(host) {
		http.Error(writer, "destination is not approved", http.StatusForbidden)
		return
	}
	select {
	case proxy.tunnels <- struct{}{}:
		defer func() { <-proxy.tunnels }()
	default:
		http.Error(writer, "proxy tunnel limit reached", http.StatusServiceUnavailable)
		return
	}

	addresses, err := proxy.resolve(request.Context(), host)
	if err != nil {
		http.Error(writer, "upstream is unavailable", http.StatusBadGateway)
		return
	}
	dialCtx, cancel := context.WithTimeout(request.Context(), clippingYouTubeProxyDialTimeout)
	defer cancel()
	var upstream net.Conn
	for _, address := range addresses {
		if dialCtx.Err() != nil {
			break
		}
		connection, dialErr := proxy.dial(dialCtx, "tcp", net.JoinHostPort(address.String(), "443"))
		if dialErr == nil && connection != nil {
			upstream = connection
			break
		}
		if connection != nil {
			_ = connection.Close()
		}
	}
	if upstream == nil {
		http.Error(writer, "upstream is unavailable", http.StatusBadGateway)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(writer, "proxy is unavailable", http.StatusInternalServerError)
		return
	}
	client, clientBuffer, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if !proxy.track(client, upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	defer proxy.untrack(client, upstream)
	defer client.Close()
	defer upstream.Close()

	_ = client.SetDeadline(proxyDeadline(request.Context()))
	_ = upstream.SetDeadline(proxyDeadline(request.Context()))
	if _, err := clientBuffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := clientBuffer.Flush(); err != nil {
		return
	}
	proxyTunnel(clientBuffer.Reader, client, upstream)
}

func (proxy *youtubeProxy) authenticated(header string) bool {
	if proxy == nil || header == "" {
		return false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	if err != nil {
		return false
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok || len(username) != len(proxy.username) || len(password) != len(proxy.password) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(username), []byte(proxy.username)) == 1 && subtle.ConstantTimeCompare([]byte(password), []byte(proxy.password)) == 1
}

func (proxy *youtubeProxy) resolve(parent context.Context, host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(parent, clippingYouTubeProxyDialTimeout)
	defer cancel()
	addresses, err := proxy.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("YouTube destination DNS resolution failed")
	}
	pinned := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if address.Zone != "" || !isPublicUnicastIP(address.IP) {
			return nil, errors.New("YouTube destination resolved unsafely")
		}
		pinned = append(pinned, append(net.IP(nil), address.IP...))
	}
	return pinned, nil
}

func (proxy *youtubeProxy) track(conns ...net.Conn) bool {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	select {
	case <-proxy.closed:
		return false
	default:
	}
	for _, conn := range conns {
		proxy.conns[conn] = struct{}{}
	}
	return true
}

func (proxy *youtubeProxy) untrack(conns ...net.Conn) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	for _, conn := range conns {
		delete(proxy.conns, conn)
	}
}

func proxyDeadline(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(clippingYouTubeExtractorTimeout)
}

func proxyTunnel(clientReader io.Reader, client, upstream net.Conn) {
	upstreamReader := bufio.NewReader(upstream)
	forwarded := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		if closer, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		forwarded <- struct{}{}
	}()
	_, _ = io.Copy(client, upstreamReader)
	_ = client.Close()
	_ = upstream.Close()
	select {
	case <-forwarded:
	case <-time.After(2 * time.Second):
	}
}

func isYouTubeProxyHost(host string) bool {
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtube-nocookie.com", "www.youtube-nocookie.com",
		"youtubei.googleapis.com", "youtube.googleapis.com", "jnn-pa.googleapis.com":
		return true
	default:
		return isYouTubeCDNHost(host)
	}
}
