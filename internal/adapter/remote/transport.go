// Package remote owns the private connection transport, not product semantics.
package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

type Identity struct {
	Location   contract.Location `json:"location"`
	PrivateKey string            `json:"private_key"`
}

func NewIdentity(endpoint, composition string) (Identity, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return Identity{}, errors.New("服务需要可达的 HTTPS 地址")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Identity{}, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Ownward"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true, IsCA: true}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		cert.IPAddresses = []net.IP{ip}
	} else {
		cert.DNSNames = []string{u.Hostname()}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		return Identity{}, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return Identity{}, err
	}
	digest := sha256.Sum256(der)
	l := contract.Location{ServiceID: hex.EncodeToString(digest[:]), Endpoint: endpoint, Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Composition: composition}
	if err := l.Validate(); err != nil {
		return Identity{}, err
	}
	return Identity{Location: l, PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}, nil
}

func (i Identity) TLSCertificate() (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(i.Location.Certificate), []byte(i.PrivateKey))
}

func Certificate(l contract.Location) (*x509.Certificate, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(l.Certificate))
	if block == nil {
		return nil, errors.New("服务信任证书无效")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(cert.Raw)
	if l.ServiceID != hex.EncodeToString(sum[:]) {
		return nil, errors.New("服务身份与可信证书不匹配")
	}
	return cert, nil
}

func Client(l contract.Location, identity *Identity) (*http.Client, error) {
	cert, err := Certificate(l)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}
	if identity != nil {
		c, err := identity.TLSCertificate()
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{c}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = config
	return &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("连接不能把凭据重定向到其他地址")
	}}, nil
}

func (i Identity) Sign(value any) (string, error) {
	cert, err := i.TLSCertificate()
	if err != nil {
		return "", err
	}
	key, ok := cert.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("服务签名密钥格式无效")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(ed25519.Sign(key, data)), nil
}
func Verify(l contract.Location, value any, signature string) error {
	cert, err := Certificate(l)
	if err != nil {
		return err
	}
	public, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return errors.New("服务签名格式无效")
	}
	sig, err := hex.DecodeString(signature)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, data, sig) {
		return errors.New("交接签名无效")
	}
	return nil
}

func Shutdown(ctx context.Context, s *http.Server) error {
	limited, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.Shutdown(limited)
}
