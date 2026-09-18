package pki

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	nodeKeyName  = "private.key"
	nodePubName  = "public.pem"
	nodeCSRName  = "node.csr"
	nodeCertName = "node.crt"
	nodeCAName   = "ca.crt"
)

// NodeMaterial is the per-node identity stored under ~/.houdry/node/.
type NodeMaterial struct {
	Dir     string
	Key     crypto.Signer
	Pub     crypto.PublicKey
	CSRPEM  []byte
	CertPEM []byte
	CAPEM   []byte
}

func NodeDir(houdryHome string) string {
	return filepath.Join(houdryHome, "node")
}

// LoadNode reads existing node identity files. Missing certs are OK.
func LoadNode(dir string) (*NodeMaterial, error) {
	key, err := loadKey(filepath.Join(dir, nodeKeyName))
	if err != nil {
		return nil, err
	}
	m := &NodeMaterial{
		Dir: dir,
		Key: key,
		Pub: key.Public(),
	}
	if b, err := os.ReadFile(filepath.Join(dir, nodeCSRName)); err == nil {
		m.CSRPEM = b
	}
	if b, err := os.ReadFile(filepath.Join(dir, nodeCertName)); err == nil {
		m.CertPEM = b
	}
	if b, err := os.ReadFile(filepath.Join(dir, nodeCAName)); err == nil {
		m.CAPEM = b
	}
	return m, nil
}

// EnsureNode generates ECDSA P-256 keys + CSR when they do not exist.
// Ed25519 keys from 0.6.8 are rotated so they can enroll against a P-256 CA.
func EnsureNode(dir, nodeID, hostname string) (*NodeMaterial, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, nodeKeyName)
	if fileExists(keyPath) {
		m, err := LoadNode(dir)
		if err != nil {
			return nil, err
		}
		if isP256(m.Key) {
			if len(m.CSRPEM) == 0 {
				csr, err := CreateCSR(m.Key, nodeID, hostname)
				if err != nil {
					return nil, err
				}
				m.CSRPEM = csr
				if err := os.WriteFile(filepath.Join(dir, nodeCSRName), csr, certPerm); err != nil {
					return nil, err
				}
			}
			return m, nil
		}
	}
	priv, err := generateP256()
	if err != nil {
		return nil, err
	}
	csr, err := CreateCSR(priv, nodeID, hostname)
	if err != nil {
		return nil, err
	}
	pubPEM, err := marshalPublicPEM(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, encodeKeyPEM(priv), keyPerm); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, nodePubName), pubPEM, certPerm); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, nodeCSRName), csr, certPerm); err != nil {
		return nil, err
	}
	return &NodeMaterial{Dir: dir, Key: priv, Pub: &priv.PublicKey, CSRPEM: csr}, nil
}

func CreateCSR(key crypto.Signer, nodeID, hostname string) ([]byte, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node ID is required in CSR")
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:         nodeID,
			Organization:       []string{"Houdry"},
			OrganizationalUnit: []string{"os=" + runtime.GOOS, "arch=" + runtime.GOARCH},
		},
	}
	if hostname != "" {
		tmpl.DNSNames = []string{hostname}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func (m *NodeMaterial) SaveEnrollment(certPEM, caPEM []byte) error {
	if err := os.WriteFile(filepath.Join(m.Dir, nodeCertName), certPEM, certPerm); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.Dir, nodeCAName), caPEM, certPerm); err != nil {
		return err
	}
	m.CertPEM, m.CAPEM = certPEM, caPEM
	return nil
}

func (m *NodeMaterial) HasCertificate() bool {
	return len(m.CertPEM) > 0 && len(m.CAPEM) > 0
}

func (m *NodeMaterial) KeyPEM() []byte { return encodeKeyPEM(m.Key) }

func (m *NodeMaterial) Certificate() (*x509.Certificate, error) {
	if len(m.CertPEM) == 0 {
		return nil, fmt.Errorf("node has no certificate")
	}
	return ParseCertPEM(m.CertPEM)
}

func marshalPublicPEM(pub crypto.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}
