package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
)

func main() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	b64 := base64.StdEncoding.EncodeToString(der)
	sum := sha256.Sum256(der)
	// Chromium 扩展 ID：SHA-256 的前 16 字节，0-9a-f 映射为 a-p
	var id []byte
	for _, b := range sum[:16] {
		id = append(id, "abcdefghijklmnop"[b>>4], "abcdefghijklmnop"[b&0x0f])
	}
	fmt.Printf("key: %s\nid: %s\n", b64, string(id))
}
