// Package identity 管理设备持久身份（Ed25519 + 自签证书）、
// TLS 双向指纹互认与可信设备库。
package identity

import (
	"crypto"
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
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppDir 存放身份、信任库与历史记录。
func AppDir() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return filepath.Join(base, "Drop")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".drop"
	}
	return filepath.Join(home, ".drop")
}

// Identity 本机持久设备身份。设备 ID 即证书指纹，重建身份等于更换 ID。
type Identity struct {
	Name    string `json:"name"`
	KeyPEM  string `json:"key_pem"`
	CertPEM string `json:"cert_pem"`

	cert *x509.Certificate
	key  crypto.Signer
}

func LoadOrCreate() (*Identity, error) {
	p := filepath.Join(AppDir(), "identity.json")
	if raw, err := os.ReadFile(p); err == nil {
		var id Identity
		if err := json.Unmarshal(raw, &id); err != nil {
			return nil, fmt.Errorf("identity.json 损坏: %w", err)
		}
		if err := id.load(); err != nil {
			return nil, err
		}
		return &id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return create(p)
}

func create(path string) (*Identity, error) {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "PC"
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"drop"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	id := &Identity{
		Name:    name,
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})),
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		cert:    cert,
		key:     key,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

func (id *Identity) load() error {
	cb, _ := pem.Decode([]byte(id.CertPEM))
	if cb == nil {
		return errors.New("证书 PEM 无效")
	}
	kb, _ := pem.Decode([]byte(id.KeyPEM))
	if kb == nil {
		return errors.New("私钥 PEM 无效")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return err
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return err
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return errors.New("不支持的私钥类型")
	}
	id.cert, id.key = cert, ed
	return nil
}

func (id *Identity) Cert() *x509.Certificate { return id.cert }

// SetName 修改本机设备名（宣告与显示用）。不改证书，因此设备 ID 不变。
func (id *Identity) SetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("设备名不能为空")
	}
	id.Name = name
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(AppDir(), "identity.json"), b, 0o600)
}

// 设备数量上限的解锁标记：仅为本机使用体验而设，点击即永久生效。
func unlockedFlag() string { return filepath.Join(AppDir(), "unlocked.flag") }

func IsUnlocked() bool {
	_, err := os.Stat(unlockedFlag())
	return err == nil
}

func SetUnlocked() error {
	// 目录可能还不存在：全新数据目录下用户可能先点解锁再配对，
	// 而 AppDir() 指向的子目录由身份创建时才建（曾直接 WriteFile 失败）。
	if err := os.MkdirAll(filepath.Dir(unlockedFlag()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(unlockedFlag(), []byte("ok"), 0o600)
}

// ID 返回本机设备 ID（证书 SHA-256 指纹，64 位大写 hex）。
func (id *Identity) ID() string { return Fingerprint(id.cert) }

// Fingerprint 计算证书指纹。
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (id *Identity) tlsCert() tls.Certificate {
	return tls.Certificate{Certificate: [][]byte{id.cert.Raw}, PrivateKey: id.key}
}
