package cmd

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"net"
	"time"

	"github.com/direct-connect/go-dc/keyprint"
)

type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

func (c *TLSConfig) Load() (cert, key []byte, _ error) {
	var err error
	cert, err = ioutil.ReadFile(c.Cert)
	if err != nil {
		return
	}
	key, err = ioutil.ReadFile(c.Key)
	return
}

func (c *TLSConfig) Generate(host string) (cert, key []byte, _ error) {
	// generate a new key-pair
	rootKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	rootCertTmpl, err := CertTemplate()
	if err != nil {
		return nil, nil, err
	}
	// describe what the certificate will be used for
	rootCertTmpl.IsCA = true
	rootCertTmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	rootCertTmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	if ip := net.ParseIP(host); ip != nil {
		rootCertTmpl.IPAddresses = []net.IP{ip}
	} else {
		rootCertTmpl.DNSNames = []string{host}
	}

	_, rootCertPEM, err := CreateCert(rootCertTmpl, rootCertTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating cert: %v", err)
	}

	// PEM encode the private key
	rootKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rootKey),
	})

	err = ioutil.WriteFile(c.Cert, rootCertPEM, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("error writing cert: %v", err)
	}
	err = ioutil.WriteFile(c.Key, rootKeyPEM, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("error writing key: %v", err)
	}

	return rootCertPEM, rootKeyPEM, nil
}

func loadCert(conf *Config) (*tls.Certificate, string, error) {
	tc := conf.Serve.TLS
	var (
		cert, key []byte
		err       error
	)
	if tc != nil {
		cert, key, err = tc.Load()
		log.Println("using certs:", tc.Cert, tc.Key)
	} else {
		tc = &TLSConfig{
			Cert: "hub.cert",
			Key:  "hub.key",
		}
		conf.Serve.TLS = tc
		cert, key, err = tc.Generate(conf.Serve.Host)
		log.Println("generated cert for", conf.Serve.Host)
	}
	if err != nil {
		return nil, "", err
	}

	// Create a TLS cert using the private key and certificate
	rootTLSCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, "", err
	}
	kp := ""
	if len(rootTLSCert.Certificate) != 0 {
		kp = keyprint.FromBytes(rootTLSCert.Certificate[0])
	}
	return &rootTLSCert, kp, nil
}

// helper function to create a cert template with a serial number and other required fields
func CertTemplate() (*x509.Certificate, error) {
	// generate a random serial number (a real cert authority would have some logic behind this)
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, errors.New("failed to generate serial number: " + err.Error())
	}

	tmpl := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{Organization: []string{"Go Hub"}},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour * 24 * 356),
		BasicConstraintsValid: true,
	}
	return &tmpl, nil
}

func CreateCert(template, parent *x509.Certificate, pub interface{}, parentPriv interface{}) (
	cert *x509.Certificate, certPEM []byte, err error) {

	certDER, err := x509.CreateCertificate(rand.Reader, template, parent, pub, parentPriv)
	if err != nil {
		return
	}
	// parse the resulting certificate so we can use it again
	cert, err = x509.ParseCertificate(certDER)
	if err != nil {
		return
	}
	// PEM encode the certificate (this is a standard TLS encoding)
	b := pem.Block{Type: "CERTIFICATE", Bytes: certDER}
	certPEM = pem.EncodeToMemory(&b)
	return
}
