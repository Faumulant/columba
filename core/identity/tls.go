package identity

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"drop/core/protocol"
)

// ServerConfig 返回要求客户端出示设备证书的服务端 TLS 配置。
// 是否信任该设备由 HTTP 层按端点判定：配对端点对未配对设备开放，
// 数据端点要求已配对。认证（证书）在握手层，授权（信任）在应用层。
func (id *Identity) ServerConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.tlsCert()},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientConfig 返回按证书指纹 pin 住对端身份的客户端 TLS 配置。
// peerID 为空时不校验指纹（仅限调试用 -addr 直连且未指定目标 ID 的场景）。
func (id *Identity) ClientConfig(peerID string) *tls.Config {
	want := protocol.NormalizeID(peerID)
	return &tls.Config{
		Certificates:       []tls.Certificate{id.tlsCert()},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // 以指纹 pin 代替 CA 链校验
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("对端未提供设备证书")
			}
			c, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			got := Fingerprint(c)
			if want != "" && got != want {
				return fmt.Errorf("设备指纹不匹配：期望 %s，实际 %s",
					protocol.DisplayID(want), protocol.DisplayID(got))
			}
			return nil
		},
	}
}
