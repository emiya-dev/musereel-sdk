package musereelsdk

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"
)

type testCertificate struct {
	certPEM []byte
	keyPEM  []byte
	serial  string
}

func makeTestCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey(CA): %v", err)
	}
	serial := big.NewInt(100)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "SDK-002 Test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate(CA): %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}
	return parsed, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func makeTestLeaf(t *testing.T, ca *x509.Certificate, caKey *rsa.PrivateKey, serial int64, commonName string, client bool) testCertificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey(%s): %v", commonName, err)
	}
	usage := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if client {
		usage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     []string{commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate(%s): %v", commonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey(%s): %v", commonName, err)
	}
	return testCertificate{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		serial:  big.NewInt(serial).String(),
	}
}

func TestMTLSConfigReloadsClientCertificatePerHandshake(t *testing.T) {
	ca, caKey, caPEM := makeTestCA(t)
	serverCertificate := makeTestLeaf(t, ca, caKey, 200, "server.local", false)
	clientOne := makeTestLeaf(t, ca, caKey, 201, "client.local", true)
	clientTwo := makeTestLeaf(t, ca, caKey, 202, "client.local", true)
	directory := t.TempDir()
	certPath := directory + "/client.crt"
	keyPath := directory + "/client.key"
	caPath := directory + "/ca.crt"
	if err := os.WriteFile(certPath, clientOne.certPEM, 0o600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(keyPath, clientOne.keyPEM, 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	config, err := NewTLSConfig(MTLSConfig{
		CertFile:   certPath,
		KeyFile:    keyPath,
		CAFile:     caPath,
		ServerName: "server.local",
	})
	if err != nil {
		t.Fatalf("NewTLSConfig: %v", err)
	}
	if config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2 (%d)", config.MinVersion, tls.VersionTLS12)
	}
	if config.GetClientCertificate == nil {
		t.Fatal("GetClientCertificate is nil")
	}
	serverPool := x509.NewCertPool()
	if !serverPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("server CA pool rejected test CA")
	}
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{mustTLSCertificate(t, serverCertificate)},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    serverPool,
		MinVersion:   tls.VersionTLS12,
	}

	gotSerial := func(t *testing.T) string {
		t.Helper()
		clientConn, serverConn := net.Pipe()
		server := tls.Server(serverConn, serverTLS.Clone())
		serverResult := make(chan struct {
			serial string
			err    error
		}, 1)
		go func() {
			err := server.Handshake()
			serial := ""
			if err == nil {
				peers := server.ConnectionState().PeerCertificates
				if len(peers) == 1 {
					serial = peers[0].SerialNumber.String()
				}
			}
			serverResult <- struct {
				serial string
				err    error
			}{serial: serial, err: err}
			_ = serverConn.Close()
		}()
		client := tls.Client(clientConn, config)
		clientErr := client.Handshake()
		_ = clientConn.Close()
		result := <-serverResult
		if clientErr != nil {
			t.Fatalf("client handshake: %v", clientErr)
		}
		if result.err != nil {
			t.Fatalf("server handshake: %v", result.err)
		}
		return result.serial
	}

	if got := gotSerial(t); got != clientOne.serial {
		t.Fatalf("first peer serial = %q, want %q", got, clientOne.serial)
	}
	if err := os.WriteFile(certPath, clientTwo.certPEM, 0o600); err != nil {
		t.Fatalf("replace client cert: %v", err)
	}
	if err := os.WriteFile(keyPath, clientTwo.keyPEM, 0o600); err != nil {
		t.Fatalf("replace client key: %v", err)
	}
	if got := gotSerial(t); got != clientTwo.serial {
		t.Fatalf("rotated peer serial = %q, want %q", got, clientTwo.serial)
	}
}

func TestMTLSConfigFailsClosedForMissingOrInvalidCA(t *testing.T) {
	directory := t.TempDir()
	cert := directory + "/client.crt"
	key := directory + "/client.key"
	if err := os.WriteFile(cert, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(key, []byte("not a key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if _, err := NewTLSConfig(MTLSConfig{CertFile: cert, KeyFile: key, CAFile: directory + "/missing.ca"}); err == nil {
		t.Fatal("missing CA was accepted")
	}
	ca := directory + "/invalid.ca"
	if err := os.WriteFile(ca, []byte("not a CA"), 0o600); err != nil {
		t.Fatalf("write invalid CA: %v", err)
	}
	if _, err := NewTLSConfig(MTLSConfig{CertFile: cert, KeyFile: key, CAFile: ca}); err == nil {
		t.Fatal("invalid CA was accepted")
	}
}

func mustTLSCertificate(t *testing.T, certificate testCertificate) tls.Certificate {
	t.Helper()
	parsed, err := tls.X509KeyPair(certificate.certPEM, certificate.keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return parsed
}
