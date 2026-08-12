package deviceidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const (
	identitySchemaVersion = 1
	maxIdentityBytes      = 64 << 10
)

type identityDocument struct {
	SchemaVersion  int    `json:"schema_version"`
	CertificatePEM string `json:"certificate_pem"`
	PrivateKeyPEM  string `json:"private_key_pem"`
}

type Identity struct {
	certificatePEM []byte
	privateKeyPEM  []byte
	fingerprint    string
}

func LoadOrCreate(path string) (*Identity, error) {
	if path == "" {
		return nil, errors.New("device identity path is required")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	lock, err := protectedfile.AcquireDirectoryLock(parent, ".identity.lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		identity, generateErr := generate()
		if generateErr != nil {
			return nil, generateErr
		}
		if writeErr := write(path, identity); writeErr != nil {
			return nil, writeErr
		}
		return identity, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect device identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("device identity must be a regular non-symlink file")
	}
	if err = protectedfile.EnsurePrivateFile(path); err != nil {
		return nil, err
	}
	if info.Size() > maxIdentityBytes {
		return nil, errors.New("device identity file is too large")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read device identity: %w", err)
	}
	var document identityDocument
	if err = protocol.DecodeStrictDocumentLimit(contents, maxIdentityBytes, &document); err != nil {
		return nil, fmt.Errorf("decode device identity: %w", err)
	}
	if document.SchemaVersion != identitySchemaVersion ||
		document.CertificatePEM == "" || document.PrivateKeyPEM == "" {
		return nil, errors.New("unsupported or incomplete device identity")
	}
	return parse([]byte(document.CertificatePEM), []byte(document.PrivateKeyPEM))
}

func (identity *Identity) Fingerprint() string { return identity.fingerprint }

func (identity *Identity) TLSCertificate() (tls.Certificate, error) {
	return tls.X509KeyPair(identity.certificatePEM, identity.privateKeyPEM)
}

func generate() (*Identity, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Device Hub private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate Device Hub certificate serial: %w", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "S3 RLCD Deck Companion Device Hub",
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"s3deck-companion.local"},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create Device Hub certificate: %w", err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode Device Hub private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	return parse(certificatePEM, privateKeyPEM)
}

func parse(certificatePEM []byte, privateKeyPEM []byte) (*Identity, error) {
	pair, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse Device Hub certificate identity: %w", err)
	}
	if len(pair.Certificate) != 1 {
		return nil, errors.New("Device Hub identity must contain exactly one certificate")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse Device Hub leaf certificate: %w", err)
	}
	if time.Now().UTC().After(certificate.NotAfter) {
		return nil, errors.New("Device Hub certificate has expired; explicit re-pairing is required")
	}
	digest := sha256.Sum256(pair.Certificate[0])
	return &Identity{
		certificatePEM: append([]byte(nil), certificatePEM...),
		privateKeyPEM:  append([]byte(nil), privateKeyPEM...),
		fingerprint:    "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func write(path string, identity *Identity) error {
	document := identityDocument{
		SchemaVersion:  identitySchemaVersion,
		CertificatePEM: string(identity.certificatePEM),
		PrivateKeyPEM:  string(identity.privateKeyPEM),
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode device identity: %w", err)
	}
	_, err = protectedfile.Replace(path, contents)
	return err
}
