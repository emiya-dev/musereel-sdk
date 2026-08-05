package musereelsdk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// MinimumTLSVersion is the explicit lower bound for SDK mTLS connections.
const MinimumTLSVersion uint16 = tls.VersionTLS12

// MTLSConfig identifies the local client certificate, private key, and CA
// bundle. The private key is read only inside the TLS stack and is never
// included in errors or formatted configuration output.
type MTLSConfig struct {
	CertFile   string
	KeyFile    string
	CAFile     string
	ServerName string
}

// NewTLSConfig loads the CA and validates the current client key pair. The
// returned config deliberately leaves Certificates empty: GetClientCertificate
// re-reads the pair for every handshake, allowing atomic file replacement to
// rotate the certificate without a watcher or background goroutine.
func NewTLSConfig(config MTLSConfig) (*tls.Config, error) {
	if config.CertFile == "" || config.KeyFile == "" || config.CAFile == "" {
		return nil, fmt.Errorf("mTLS requires certificate, key, and CA file paths")
	}
	rootCAs, err := loadCAPool(config.CAFile)
	if err != nil {
		return nil, err
	}
	if _, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile); err != nil {
		return nil, fmt.Errorf("mTLS client certificate is unavailable")
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
		ServerName: config.ServerName,
	}
	tlsConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
		if err != nil {
			// Do not include the key, certificate bytes, or parser details in a
			// transport error. A failed reload fails the handshake closed.
			return nil, fmt.Errorf("mTLS client certificate reload failed")
		}
		return &certificate, nil
	}
	return tlsConfig, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mTLS CA bundle is unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("mTLS CA bundle is invalid")
	}
	return pool, nil
}

// NewMTLSCredentials returns grpc transport credentials using NewTLSConfig.
func NewMTLSCredentials(config MTLSConfig) (credentials.TransportCredentials, error) {
	tlsConfig, err := NewTLSConfig(config)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(tlsConfig), nil
}

// DialRuntime opens an mTLS grpc connection. ExchangeRuntimeToken selects its
// temporary internal wire codec per call, so this connection remains usable
// by future generated protobuf clients as well.
func DialRuntime(ctx context.Context, target string, config MTLSConfig, options ...grpc.DialOption) (*grpc.ClientConn, error) {
	credentials, err := NewMTLSCredentials(config)
	if err != nil {
		return nil, err
	}
	options = append([]grpc.DialOption{grpc.WithTransportCredentials(credentials)}, options...)
	return grpc.DialContext(ctx, target, options...)
}
