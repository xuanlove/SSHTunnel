package ipcert

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme"
)

// issueWithACME 通过 ACME shortlived profile 为 IP 申请证书。
// 流程：注册/复用账户 → 创建 IP 订单 → 完成 http-01 验证 → 提交 CSR → 下载证书。
func (m *Manager) issueWithACME(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
	parsed, err := parsePublicIP(ip)
	if err != nil {
		return nil, nil, time.Time{}, err
	}

	accountKey, err := loadOrCreateAccountKey(filepath.Join(m.dir, "account.key"))
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("加载 ACME 账户密钥失败: %w", err)
	}

	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: s.Directory(),
	}
	acct := &acme.Account{}
	if s.Email != "" {
		acct.Contact = []string{"mailto:" + s.Email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil &&
		!errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, nil, time.Time{}, fmt.Errorf("ACME 账户注册失败: %w", err)
	}

	// IP 证书仅 shortlived profile 支持
	order, err := client.AuthorizeOrder(ctx,
		[]acme.AuthzID{{Type: "ip", Value: ip}},
		acme.WithOrderIssuanceProfile(acme.ProfileName(shortlivedProfile)),
	)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("创建 ACME 订单失败（IP 证书需 CA 支持 shortlived profile）: %w", err)
	}

	// http-01 应答服务：仅在验证期间监听
	cs, err := startChallengeServer(s.Port())
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	defer cs.Close()

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("获取授权信息失败: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == "http-01" {
				chal = c
				break
			}
		}
		if chal == nil {
			return nil, nil, time.Time{}, fmt.Errorf("CA 未提供 http-01 验证方式（IP 证书不支持 dns-01）")
		}
		keyAuth, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("生成 http-01 应答失败: %w", err)
		}
		cs.setToken(chal.Token, keyAuth)
		if _, err := client.Accept(ctx, chal); err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("提交 http-01 验证失败: %w", err)
		}
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("http-01 验证未通过: %w", err)
		}
	}

	// 生成证书私钥与 CSR（IP SAN）
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("生成证书私钥失败: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:     pkix.Name{},
		IPAddresses: []net.IP{parsed},
	}, certKey)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("生成 CSR 失败: %w", err)
	}

	// 注意：必须用 CreateCertFromOrder（内部使用 order.FinalizeURL 提交 CSR）。
	// CreateOrderCert 的首参是 finalize URL，传 order.URI 会把 CSR POST 到订单地址，
	// CA 会按 POST-as-GET 校验并返回 "must have an empty payload"。
	der, _, err := client.CreateCertFromOrder(ctx, order, csrDER, true)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("提交 CSR 并获取证书失败: %w", err)
	}
	if len(der) == 0 {
		return nil, nil, time.Time{}, fmt.Errorf("CA 返回空证书链")
	}

	var certBuf bytes.Buffer
	for _, b := range der {
		if err := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: b}); err != nil {
			return nil, nil, time.Time{}, err
		}
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("编码私钥失败: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("解析签发证书失败: %w", err)
	}
	return certBuf.Bytes(), keyPEM, leaf.NotAfter, nil
}

// loadOrCreateAccountKey 加载或创建 ACME 账户密钥（ECDSA P-256）
func loadOrCreateAccountKey(path string) (*ecdsa.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block != nil {
			if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				return key, nil
			}
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if ec, ok := key.(*ecdsa.PrivateKey); ok {
					return ec, nil
				}
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0600); err != nil {
		return nil, err
	}
	return key, nil
}
