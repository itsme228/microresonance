// Самоподписанный сертификат для HTTPS в LAN (192.168.x.x, 10.x.x.x, 127.0.0.1).
// Сертификат создаётся/обновляется с текущими IPv4 интерфейсов; при смене IP — пересоздаётся.

package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const certValidityDays = 365

// ensureTLSCert генерирует самоподписанный сертификат для IP и localhost, сохраняет в certDir.
// Если сертификат уже есть и в нём есть все текущие LAN IP — переиспользует (не пересоздаёт).
// Возвращает пути к cert/key, список IP и generated=true только если сертификат был создан за этот запуск.
func EnsureTLSCert(certDir string) (certFile, keyFile string, ipsInCert []net.IP, generated bool, err error) {
	certFile = filepath.Join(certDir, "server.crt")
	keyFile = filepath.Join(certDir, "server.key")
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return "", "", nil, false, fmt.Errorf("mkdir certs: %w", err)
	}

	wantIPs := localIPsForCert()
	needGen := true
	if data, readErr := os.ReadFile(certFile); readErr == nil {
		if block, _ := pem.Decode(data); block != nil {
			if cert, e := x509.ParseCertificate(block.Bytes); e == nil {
				ipsInCert = cert.IPAddresses
				if certHasAllIPs(cert.IPAddresses, wantIPs) {
					needGen = false
				}
			}
		}
	}
	if needGen {
		ipsInCert, err = generateTLSCert(certFile, keyFile, wantIPs)
		if err != nil {
			return "", "", nil, false, err
		}
		generated = true
	}
	return certFile, keyFile, ipsInCert, generated, nil
}

func certHasAllIPs(inCert []net.IP, want []net.IP) bool {
	for _, w := range want {
		found := false
		for _, c := range inCert {
			if c.Equal(w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// localIPsForCert возвращает 127.0.0.1 и все не-loopback IPv4 с интерфейсов (в т.ч. 192.168.1.x).
func localIPsForCert() []net.IP {
	out := []net.IP{net.ParseIP("127.0.0.1")}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
			out = append(out, ipnet.IP)
		}
	}
	return out
}

func generateTLSCert(certFile, keyFile string, ips []net.IP) (ipsInCert []net.IP, err error) {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Microresonance VR"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(0, 0, certValidityDays),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  make([]net.IP, 0, len(ips)),
		DNSNames:     []string{"localhost"},
	}
	for _, ip := range ips {
		if ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		}
	}
	ipsInCert = template.IPAddresses

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create cert: %w", err)
	}
	certOut, err := os.OpenFile(certFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("write cert: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		certOut.Close()
		return nil, err
	}
	certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		keyOut.Close()
		return nil, err
	}
	keyOut.Close()
	return ipsInCert, nil
}

// copyCertToVR копирует server.crt в vr/certs/server.crt для Godot VR. Логирует только при записи или ошибке.
func CopyCertToVR(certFile string) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TLS: copyCertToVR read %s: %v\n", certFile, err)
		return
	}
	absCert, _ := filepath.Abs(certFile)
	certDir := filepath.Dir(absCert)
	repoRoot := filepath.Dir(filepath.Dir(certDir))
	vrCerts := filepath.Join(repoRoot, "vr", "certs")
	if err := os.MkdirAll(vrCerts, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "TLS: copyCertToVR mkdir %s: %v\n", vrCerts, err)
		return
	}
	dst := filepath.Join(vrCerts, "server.crt")
	existing, _ := os.ReadFile(dst)
	if len(existing) == len(data) && string(existing) == string(data) {
		return // уже такой же — не пишем и не логируем
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "TLS: copyCertToVR write %s: %v\n", dst, err)
		return
	}
	fmt.Fprintf(os.Stderr, "TLS: cert copied to %s (for VR APK)\n", dst)
}
