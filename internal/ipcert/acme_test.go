package ipcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeACME 模拟 Let's Encrypt 的关键行为，用于回归测试 IP 证书签发流程。
// 特别地：订单地址只接受 POST-as-GET（空 payload），与 LE 的
// "POST-as-GET requests must have an empty payload" 校验一致。
type fakeACME struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	accepted bool
	leafPEM  []byte
	ip       string
	caKey    *ecdsa.PrivateKey
	caCert   *x509.Certificate
}

func newFakeACME(t *testing.T, ip string) *fakeACME {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 CA 密钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake ACME CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("生成 CA 证书失败: %v", err)
	}
	ca, _ := x509.ParseCertificate(der)
	f := &fakeACME{t: t, ip: ip, caKey: key, caCert: ca}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeACME) url(path string) string { return f.server.URL + path }

func (f *fakeACME) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Replay-Nonce", "nonce-1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// payload 解码 JWS 的 payload；POST-as-GET 返回空字符串
func payload(r *http.Request) ([]byte, error) {
	var body struct {
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Payload == "" {
		return nil, nil
	}
	return base64.RawURLEncoding.DecodeString(body.Payload)
}

func (f *fakeACME) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/directory":
		f.writeJSON(w, http.StatusOK, map[string]interface{}{
			"newNonce":   f.url("/nonce"),
			"newAccount": f.url("/new-account"),
			"newOrder":   f.url("/new-order"),
			"revokeCert": f.url("/revoke"),
			"keyChange":  f.url("/key-change"),
			"meta": map[string]interface{}{
				"profiles": map[string]string{"shortlived": f.url("/profiles/shortlived")},
			},
		})
	case r.URL.Path == "/nonce":
		w.Header().Set("Replay-Nonce", "nonce-1")
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/new-account":
		w.Header().Set("Location", f.url("/account/1"))
		f.writeJSON(w, http.StatusCreated, map[string]string{"status": "valid"})
	case r.URL.Path == "/new-order":
		b, err := payload(r)
		if err != nil {
			http.Error(w, "bad jws", http.StatusBadRequest)
			return
		}
		var req struct {
			Identifiers []struct{ Type, Value string } `json:"identifiers"`
			Profile     string                         `json:"profile"`
		}
		_ = json.Unmarshal(b, &req)
		if req.Profile != "shortlived" {
			f.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"type": "urn:ietf:params:acme:error:malformed", "detail": "profile must be shortlived",
			})
			return
		}
		w.Header().Set("Location", f.url("/order/1"))
		f.writeJSON(w, http.StatusCreated, map[string]interface{}{
			"status":         "pending",
			"identifiers":    req.Identifiers,
			"authorizations": []string{f.url("/authz/1")},
			"finalize":       f.url("/finalize/1"),
		})
	case r.URL.Path == "/authz/1":
		// POST-as-GET：必须空 payload
		if b, _ := payload(r); len(b) > 0 {
			f.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"type": "urn:ietf:params:acme:error:malformed", "detail": "POST-as-GET requests must have an empty payload",
			})
			return
		}
		f.mu.Lock()
		status := "pending"
		if f.accepted {
			status = "valid"
		}
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":     status,
			"identifier": map[string]string{"type": "ip", "value": f.ip},
			"challenges": []map[string]interface{}{{
				"type":   "http-01",
				"url":    f.url("/chall/1"),
				"token":  "tok-1",
				"status": map[bool]string{true: "valid", false: "pending"}[status == "valid"],
			}},
		})
	case r.URL.Path == "/chall/1":
		f.mu.Lock()
		f.accepted = true
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]interface{}{
			"type": "http-01", "url": f.url("/chall/1"), "token": "tok-1", "status": "valid",
		})
	case r.URL.Path == "/order/1":
		// 与 Let's Encrypt 一致：订单地址只接受 POST-as-GET
		if b, _ := payload(r); len(b) > 0 {
			f.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"type": "urn:ietf:params:acme:error:malformed", "detail": "POST-as-GET requests must have an empty payload",
			})
			return
		}
		f.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      "valid",
			"certificate": f.url("/cert/1"),
		})
	case r.URL.Path == "/finalize/1":
		b, err := payload(r)
		if err != nil || len(b) == 0 {
			http.Error(w, "bad finalize", http.StatusBadRequest)
			return
		}
		var req struct {
			CSR string `json:"csr"`
		}
		_ = json.Unmarshal(b, &req)
		der, err := base64.RawURLEncoding.DecodeString(req.CSR)
		if err != nil {
			http.Error(w, "bad csr", http.StatusBadRequest)
			return
		}
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			http.Error(w, "bad csr", http.StatusBadRequest)
			return
		}
		leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject:      pkix.Name{CommonName: f.ip},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(6 * 24 * time.Hour),
			IPAddresses:  []net.IP{net.ParseIP(f.ip)},
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}, f.caCert, csr.PublicKey, f.caKey)
		if err != nil {
			http.Error(w, "sign failed", http.StatusInternalServerError)
			return
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})
		f.mu.Lock()
		f.leafPEM = pemBytes
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      "valid",
			"certificate": f.url("/cert/1"),
		})
	case r.URL.Path == "/cert/1":
		if b, _ := payload(r); len(b) > 0 {
			f.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"type": "urn:ietf:params:acme:error:malformed", "detail": "POST-as-GET requests must have an empty payload",
			})
			return
		}
		f.mu.Lock()
		chain := append(append([]byte{}, f.leafPEM...),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caCert.Raw})...)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		w.Header().Set("Replay-Nonce", "nonce-1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chain)
	default:
		http.NotFound(w, r)
	}
}

// TestIssueWithACMEAgainstFakeCA 回归：CSR 必须提交到 finalize 地址。
// 若误用 CreateOrderCert(order.URI) 把 CSR POST 到订单地址，将命中
// "POST-as-GET requests must have an empty payload" 而失败。
func TestIssueWithACMEAgainstFakeCA(t *testing.T) {
	const ip = "203.0.113.10"
	acmeSrv := newFakeACME(t, ip)

	// http-01 用随机空闲端口，避免依赖 80
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	m := NewManager(t.TempDir(), nil)
	s := Settings{Enabled: true, IP: ip, HTTPPort: port, DirectoryURL: acmeSrv.url("/directory")}
	if err := m.ApplySettings(Settings{Enabled: false, IP: ip, HTTPPort: port, DirectoryURL: s.DirectoryURL}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	m.mu.Lock()
	m.settings.Enabled = true
	m.mu.Unlock()

	if err := m.IssueNow(t.Context()); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	cert, err := m.TLSCertificate()
	if err != nil {
		t.Fatalf("获取证书失败: %v", err)
	}
	if cert.Leaf == nil || len(cert.Leaf.IPAddresses) == 0 || cert.Leaf.IPAddresses[0].String() != ip {
		t.Errorf("证书 IP 不正确: %+v", cert.Leaf)
	}
	st := m.Status()
	if st.LastError != "" || st.NotAfter.IsZero() {
		t.Errorf("状态异常: %+v", st)
	}
	if !strings.HasSuffix(st.CertPath, "cert.pem") {
		t.Errorf("证书路径异常: %s", st.CertPath)
	}
}
