package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"time"
)

func genTLSCACert(conf *config) error {
	// Generate CA private key
	caKeyBytes, caKey, err := tlsGenKey(conf.SSLKeyCurve)
	if err != nil {
		return fmt.Errorf("failed to generate ca private key: %v", err)
	}

	// Set our CA cert validity constraints
	notBefore := time.Now()
	notAfter := notBefore.Add(time.Duration(conf.SSLCADuration) * 24 * time.Hour)

	// Set our serial to 1
	serial := big.NewInt(1)

	// Set our CA cert options
	opts := certOpts{
		CAKey:     caKey,
		CN:        "curse",
		IsCA:      true,
		NotBefore: notBefore,
		NotAfter:  notAfter,
		SAN:       conf.SSLCertHostname,
		Serial:    serial,
	}

	// Sign the CA cert
	caCert, _, err := tlsSignCert(opts)
	if err != nil {
		return fmt.Errorf("failed to generate ca cert: %v", err)
	}

	err = ioutil.WriteFile(conf.SSLKey, caKeyBytes, 0600)
	if err != nil {
		return fmt.Errorf("failed to write ca private key file: %v", err)
	}

	err = ioutil.WriteFile(conf.SSLCert, caCert, 0644)
	if err != nil {
		return fmt.Errorf("failed to write cert file: %v", err)
	}
	err = ioutil.WriteFile(conf.SSLCA, caCert, 0644)
	if err != nil {
		return fmt.Errorf("failed to write ca cert file: %v", err)
	}

	// Update our CA's serial index
	return dbSetTLSSerial(conf, serial)
}

func signTLSClientCert(conf *config, csr *x509.CertificateRequest) ([]byte, []byte, error) {
	// Set our cert validity constraints
	notBefore := time.Now()
	notAfter := notBefore.Add(conf.tlsDur)

	// Get the next available serial number
	serial, err := dbIncTLSSerial(conf)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate client certficate: %v", err)
	}

	// Set our CA cert options
	opts := certOpts{
		CA:        conf.tlsCACert,
		CAKey:     conf.tlsCAKey,
		CSR:       csr,
		IsCA:      false,
		NotBefore: notBefore,
		NotAfter:  notAfter,
		Serial:    serial,
	}

	// Sign the CA cert
	pemCert, rawCert, err := tlsSignCert(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate client cert: %v", err)
	}

	return pemCert, rawCert, nil
}

func initTLSCerts(conf *config) (bool, error) {
	var err error

	_, errK := os.Stat(conf.SSLKey)
	_, errC := os.Stat(conf.SSLCert)
	if os.IsNotExist(errK) && os.IsNotExist(errC) {
		// Generate CA/server key/cert
		err = genTLSCACert(conf)
		if err != nil {
			return false, err
		}
	}
	if os.IsNotExist(errK) && !os.IsNotExist(errC) {
		return false, fmt.Errorf("error initializing ca certificate: sslcert exists, but sslkey does not")
	}

	if _, err = os.Stat(conf.SSLCA); os.IsNotExist(err) {
		conf.SSLCA = conf.SSLCert
		return true, fmt.Errorf("discrete ca cert not supported with automatic cert generation. Using sslcert file as ca cert: %s", conf.SSLCert)
	}

	// Load our CA cert/key for signing
	err = loadTLSCA(conf)
	if err != nil {
		return false, err
	}

	return true, nil
}

func loadTLSCA(conf *config) error {
	// Load CA key for signing
	caKeyPem, err := ioutil.ReadFile(conf.SSLKey)
	if err != nil {
		return fmt.Errorf("failed to read tls key file: '%v'", err)
	}
	caKey, _ := pem.Decode(caKeyPem)
	if caKey == nil {
		return fmt.Errorf("failed to parse tls key file: '%v'", err)
	}
	conf.tlsCAKey, err = x509.ParseECPrivateKey(caKey.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse tls cert file: '%v'", err)
	}

	// Load CA cert for signing
	caCertPem, err := ioutil.ReadFile(conf.SSLCert)
	if err != nil {
		return fmt.Errorf("failed to read tls cert file: '%v'", err)
	}
	caCert, _ := pem.Decode(caCertPem)
	if caCert == nil {
		return fmt.Errorf("failed to decode tls cert file: '%v'", err)
	}
	conf.tlsCACert, err = x509.ParseCertificate(caCert.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse tls cert file: '%v'", err)
	}

	return nil
}
