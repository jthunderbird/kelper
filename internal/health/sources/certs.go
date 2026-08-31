package sources

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"k8s.io/client-go/rest"
)

// certDialTimeout bounds each TLS handshake.
const certDialTimeout = 3 * time.Second

// kubeletPort is the kubelet's HTTPS port, whose serving certificate is the one
// that expires and locks the control plane out of the node.
const kubeletPort = "10250"

// CertInfo is a certificate's identity and expiry.
type CertInfo struct {
	// Endpoint describes what was probed, e.g. "apiserver" or "kubelet/node-1".
	Endpoint string
	Subject  string
	NotAfter time.Time
}

// Expired reports whether the certificate is already invalid.
func (c CertInfo) Expired() bool { return time.Now().After(c.NotAfter) }

// Remaining is how long until the certificate expires; negative when expired.
func (c CertInfo) Remaining() time.Duration { return time.Until(c.NotAfter) }

// FetchCerts collects certificate expiry for the API server, every node's
// kubelet, and the client certificate in the active kubeconfig.
//
// Expiry is read by TLS handshake rather than from kubelet metrics: the
// kubelet_certificate_manager_*_ttl_seconds gauges only exist where the kubelet
// certificate manager is in use, and are absent on distributions that manage
// certificates as files (RKE2 among them). A handshake works everywhere.
func FetchCerts(ctx context.Context, cfg *rest.Config, nodeAddrs map[string]string) []CertInfo {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []CertInfo
	)
	add := func(c CertInfo) {
		mu.Lock()
		out = append(out, c)
		mu.Unlock()
	}

	if cfg != nil && cfg.Host != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if hp, err := hostPort(cfg.Host, "443"); err == nil {
				if info, err := peerCert(ctx, hp); err == nil {
					info.Endpoint = "apiserver"
					add(info)
				}
			}
		}()

		// The client certificate is the credential that locks the user out when
		// it lapses, and it is not observable from any server endpoint.
		if info, ok := clientCert(cfg); ok {
			add(info)
		}
	}

	for name, addr := range nodeAddrs {
		wg.Add(1)
		go func(name, addr string) {
			defer wg.Done()
			info, err := peerCert(ctx, net.JoinHostPort(addr, kubeletPort))
			if err != nil {
				return
			}
			info.Endpoint = "kubelet/" + name
			add(info)
		}(name, addr)
	}

	wg.Wait()
	return out
}

// peerCert completes a TLS handshake and returns the leaf certificate's
// identity. Verification is deliberately skipped: reading an expiry must work
// against the self-signed CAs that clusters normally use, and no data from the
// peer is trusted beyond its notAfter and subject.
func peerCert(ctx context.Context, hostPort string) (CertInfo, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: certDialTimeout},
		Config:    &tls.Config{InsecureSkipVerify: true},
	}
	ctx, cancel := context.WithTimeout(ctx, certDialTimeout)
	defer cancel()

	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return CertInfo{}, err
	}
	defer conn.Close()

	tc, ok := conn.(*tls.Conn)
	if !ok {
		return CertInfo{}, fmt.Errorf("not a TLS connection")
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return CertInfo{}, fmt.Errorf("no peer certificate")
	}
	return CertInfo{Subject: certs[0].Subject.String(), NotAfter: certs[0].NotAfter}, nil
}

// clientCert reads the client certificate from the rest config, whether it was
// supplied inline or as a file path.
func clientCert(cfg *rest.Config) (CertInfo, bool) {
	raw := cfg.CertData
	if len(raw) == 0 && cfg.CertFile != "" {
		var err error
		if raw, err = readFile(cfg.CertFile); err != nil {
			return CertInfo{}, false
		}
	}
	if len(raw) == 0 {
		return CertInfo{}, false
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return CertInfo{}, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return CertInfo{}, false
	}
	return CertInfo{
		Endpoint: "kubeconfig client cert",
		Subject:  cert.Subject.String(),
		NotAfter: cert.NotAfter,
	}, true
}

// hostPort normalizes a server URL into host:port, applying a default port.
func hostPort(server, defaultPort string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	host := u.Host
	if host == "" {
		host = server
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, defaultPort)
	}
	return host, nil
}
