package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
)

type tlsParams struct {
	bastionUser string
	csr         string
	userIP      string
}

func tlsCertHandler(w http.ResponseWriter, r *http.Request, conf *config) {
	// Verify we're receiving this request from the cert broker
	if len(r.TLS.PeerCertificates) > 0 {
		fp := tlsCertFP(r.TLS.PeerCertificates[0])
		if bytes.Compare(conf.brokerFP, fp) != 0 {
			log.Printf("Not authorized to generate certificates: ip[%s] user[%s] cert[%s]", r.RemoteAddr, r.TLS.PeerCertificates[0].Subject.CommonName, fp)
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}
	} else {
		log.Printf("Invalid connection")
		http.Error(w, "Invalid connection", http.StatusBadRequest)
		return
	}

	// Load our form parameters into a struct
	p := tlsParams{
		bastionUser: r.Header.Get(conf.UserHeader),
		csr:         r.PostFormValue("csr"),
		userIP:      r.PostFormValue("userIP"),
	}

	// Make sure we have everything we need from our parameters
	err := validateTLSParams(p, conf)
	if err != nil {
		errMsg := fmt.Sprintf("Param validation failure: %v", err)
		log.Printf(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	// Decode our pem-encapsulated CSR
	csrBlock, _ := pem.Decode([]byte(p.csr))
	if csrBlock == nil {
		errMsg := fmt.Sprintf("Failed to decode CSR: '%v'", err)
		log.Printf(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	// Parse the CSR
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to parse CSR: %v", err)
		log.Printf(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	// Validate the CSR signature
	err = csr.CheckSignature()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to check CSR signature: %v", err)
		log.Printf(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	// Check if our username received matches the CSR name
	if csr.Subject.CommonName != p.bastionUser {
		errMsg := fmt.Sprintf("CSR CommonName field does not match logged-in user, denying request")
		log.Printf(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	// Sign the CSR
	cert, rawCert, err := signTLSClientCert(conf, csr)
	if err != nil {
		log.Printf("%v", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	// Parse the DER formatted cert
	c, err := x509.ParseCertificate(rawCert)
	if err != nil {
		log.Printf("Error parsing raw certificate: %v", err)
		http.Error(w, "Error generating certificate", http.StatusInternalServerError)
		return
	}

	// Get a public key fingerprint
	fp := tlsCertFP(c)

	// Generate our log entry
	keyID := fmt.Sprintf("user[%s] from[%s] serial[%d] fingerprint[%s]", p.bastionUser, p.userIP, c.SerialNumber, fp)

	// Log the request
	log.Printf("TLS request: %s", keyID)

	w.Write(cert)
}

func validateTLSParams(p tlsParams, conf *config) error {
	if p.bastionUser == "" {
		err := fmt.Errorf("%s missing from request", conf.UserHeader)
		return err
	} else if !conf.userRegex.MatchString(p.bastionUser) {
		err := fmt.Errorf("username is invalid")
		return err
	}
	if p.csr == "" {
		err := fmt.Errorf("csr missing from request")
		return err
	}
	if conf.RequireClientIP && !validIP(p.userIP) {
		err := fmt.Errorf("invalid userIP")
		log.Printf("invalid userIP: |%s|", p.userIP) // FIXME This should be re-evaluated in the logging refactor
		return err
	}

	return nil
}
