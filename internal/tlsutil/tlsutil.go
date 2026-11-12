// Package tlsutil creates the hub's self-signed certificate and builds
// agent TLS configs that trust the hub by public-key pin (SHA-256 of the
// SubjectPublicKeyInfo, the same format as curl --pinnedpubkey sha256//).
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

// EnsureSelfSigned loads certFile/keyFile, creating a new ECDSA P-256
// self-signed certificate valid for 10 years if they do not exist.
// hosts are IPs or DNS names put into the SAN.
func EnsureSelfSigned(certFile, keyFile string, hosts []string) (created bool, err error) {
	if _, err := os.Stat(certFile); err == nil {
		return false, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Monitor.uz hub", Organization: []string{"Monitor.uz"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return false, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return false, err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// LocalHosts returns the machine hostname and its non-loopback IPs, used
// as default SANs.
func LocalHosts() []string {
	var out []string
	if h, err := os.Hostname(); err == nil {
		out = append(out, h)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			out = append(out, ipn.IP.String())
		}
	}
	return append(out, "localhost", "127.0.0.1")
}

// SPKIPin returns base64(sha256(SubjectPublicKeyInfo)).
func SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// PinFromFile reads a PEM certificate and returns its SPKI pin.
func PinFromFile(certFile string) (string, error) {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return "", errors.New("no PEM certificate in " + certFile)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return "", err
	}
	return SPKIPin(c), nil
}

// PinnedConfig returns a client TLS config that accepts the server only if
// its leaf certificate's public key matches pin. Hostname and CA chain are
// intentionally not checked: the pin is the trust anchor, so agents keep
// working when the hub is reached by IP.
func PinnedConfig(pin string) (*tls.Config, error) {
	want, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("invalid pin %q", pin)
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyConnection below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("hub sent no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return errors.New("hub certificate does not match pinned key")
			}
			return nil
		},
	}, nil
}
