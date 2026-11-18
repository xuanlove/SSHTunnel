package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"time"
)

// errMissingTLSCert HTTPS 监听器未配置证书内容
var errMissingTLSCert = errors.New("未配置 HTTPS 证书内容")

// CertInfo 证书摘要信息（用于界面展示，不含私钥）
type CertInfo struct {
	SubjectCN string   `json:"subject_cn"` // 使用者公用名（CN）
	IssuerCN  string   `json:"issuer_cn"`  // 颁发者公用名
	NotBefore string   `json:"not_before"` // 生效时间（RFC3339）
	NotAfter  string   `json:"not_after"`  // 到期时间（RFC3339）
	DNSNames  []string `json:"dns_names,omitempty"`
	Expired   bool     `json:"expired"`   // 是否已过期
	DaysLeft  int      `json:"days_left"` // 距离到期的天数（已过期时为负数）
}

// InspectCertificate 解析证书 PEM 文本，返回使用者公用名与有效期等信息。
// 支持带完整证书链的 PEM（取第一个 CERTIFICATE 块）。
func InspectCertificate(certPEM string) (*CertInfo, error) {
	certPEM = strings.TrimSpace(certPEM)
	if certPEM == "" {
		return nil, errors.New("证书内容为空")
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("证书内容不是有效的 PEM（缺少 CERTIFICATE 块）")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("解析证书失败: " + err.Error())
	}
	now := time.Now()
	return &CertInfo{
		SubjectCN: cert.Subject.CommonName,
		IssuerCN:  cert.Issuer.CommonName,
		NotBefore: cert.NotBefore.Format(time.RFC3339),
		NotAfter:  cert.NotAfter.Format(time.RFC3339),
		DNSNames:  cert.DNSNames,
		Expired:   now.After(cert.NotAfter),
		DaysLeft:  int(cert.NotAfter.Sub(now).Hours() / 24),
	}, nil
}

// GenerateSelfSignedPEM 生成自签证书（供 HTTPS 代理使用），返回 PEM 文本。
// 证书为 ECDSA P-256、10 年有效期，SAN 包含 localhost / 127.0.0.1 / ::1。
func GenerateSelfSignedPEM() (certPEM, keyPEM string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"SSH Tunnel Manager"},
			CommonName:   "sshtunnel-proxy",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return "", "", err
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", err
	}

	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	return certPEM, keyPEM, nil
}
