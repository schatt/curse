package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/boltdb/bolt"
	"github.com/spf13/viper"
)

type config struct {
	brokerFP         []byte
	bucketNameFP     []byte
	bucketNameSerial []byte
	db               *bolt.DB
	dur              time.Duration
	exts             map[string]string
	keyLifeSpan      time.Duration
	sshCASigner      ssh.Signer
	tlsDur           time.Duration
	tlsCACert        *x509.Certificate
	tlsCAKey         *ecdsa.PrivateKey
	userRegex        *regexp.Regexp

	Addr              string
	CAKeyFile         string
	DBFile            string
	Duration          int
	Extensions        []string
	ForceCmd          bool
	MaxKeyAge         int
	Port              int
	RequireClientIP   bool
	SSLBrokerCert     string
	SSLBrokerHostname string
	SSLBrokerKey      string
	SSLCA             string
	SSLCADuration     int
	SSLCert           string
	SSLCertHostname   string
	SSLKey            string
	SSLKeyCurve       string
	SSLDuration       int
	UserHeader        string
}

func main() {
	// Process/load our config options
	conf, err := getConf()
	if err != nil {
		log.Fatal(err)
	}

	// Convert our cert validity duration and pubkey lifespan from int to time.Duration
	conf.dur = time.Duration(conf.Duration) * time.Second
	if conf.MaxKeyAge < 0 {
		// Negative MaxKeyAge means unlimited age keys, set lifespan to 100 years
		conf.keyLifeSpan = 100 * 365 * 24 * time.Hour
	} else {
		conf.keyLifeSpan = time.Duration(conf.MaxKeyAge) * 24 * time.Hour
	}

	// Convert our TLS cert "session" length and pubkey lifespan from int to time.Duration
	if conf.SSLDuration < 0 {
		// Negative SSLDuration means unlimited age keys, set lifespan to 100 years
		conf.tlsDur = 100 * 365 * 24 * time.Hour
	} else {
		conf.tlsDur = time.Duration(conf.SSLDuration) * time.Second
	}

	// Load the CA key into an ssh.Signer
	conf.sshCASigner, err = loadSSHCAKey(conf.CAKeyFile)
	if err != nil {
		log.Fatalf("%v", err)
	}

	// Open our key tracking database file
	conf.db, err = bolt.Open(conf.DBFile, 0600, nil)
	if err != nil {
		log.Fatalf("Could not open database file %v", err)
	}
	defer conf.db.Close()

	// Check TLS certs
	ok, err := initTLSCerts(conf)
	if !ok {
		log.Fatal(err)
	}
	if err != nil {
		log.Printf("%v", err)
	}

	// Get a fingerprint of the broker cert to restrict certificate signing
	conf.brokerFP, err = getBrokerFP(conf)
	if err != nil {
		log.Fatal(err)
	}

	// Set our web handler function
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		sshCertHandler(w, r, conf)
	})

	// Set our web handler function
	http.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) {
		tlsCertHandler(w, r, conf)
	})

	// Prepare our TLS settings
	addrPort := fmt.Sprintf("%s:%d", conf.Addr, conf.Port)
	tlsConf, err := getTLSConfig(conf)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:      addrPort,
		TLSConfig: tlsConf,
	}

	// Start our listener service
	log.Printf("Starting HTTPS server on %s", addrPort)
	err = server.ListenAndServeTLS(conf.SSLCert, conf.SSLKey)
	if err != nil {
		log.Fatalf("Listener service: %v", err)
	}
}

func init() {
	//if cfgFile != "" { // enable ability to specify config file via flag
	//	viper.SetConfigFile(cfgFile)
	//}

	viper.SetConfigName("cursed") // name of config file (without extension)
	viper.AddConfigPath("/opt/curse/etc/")
	viper.AddConfigPath("/etc/curse/")
	viper.AddConfigPath(".")
	viper.ReadInConfig()

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		log.Printf("Using config file: %s", viper.ConfigFileUsed())
	}

	viper.SetDefault("addr", "127.0.0.1")
	viper.SetDefault("cakeyfile", "/opt/curse/etc/user_ca")
	viper.SetDefault("dbfile", "/opt/curse/etc/cursed.db")
	viper.SetDefault("duration", 2*60) // 2 minute default
	viper.SetDefault("extensions", []string{"permit-pty"})
	viper.SetDefault("forcecmd", false)
	viper.SetDefault("maxkeyage", 90) // 90 day default
	viper.SetDefault("port", 81)
	viper.SetDefault("requireclientip", true)
	viper.SetDefault("sslbrokercert", "/opt/curse/etc/broker.crt")
	viper.SetDefault("sslbrokerhostname", "localhost")
	viper.SetDefault("sslbrokerkey", "/opt/curse/etc/broker.key")
	viper.SetDefault("sslca", "/opt/curse/etc/cursed.crt")
	viper.SetDefault("sslcaduration", 730) // 2 year default
	viper.SetDefault("sslcert", "/opt/curse/etc/cursed.crt")
	viper.SetDefault("sslcerthostname", "localhost")
	viper.SetDefault("sslkey", "/opt/curse/etc/cursed.key")
	viper.SetDefault("sslkeycurve", "p384")
	viper.SetDefault("sslduration", 12*60) // 12 hour default
	viper.SetDefault("userheader", "REMOTE_USER")
}

func validateExtensions(confExts []string) (map[string]string, []error) {
	validExts := []string{"permit-X11-forwarding", "permit-agent-forwarding",
		"permit-port-forwarding", "permit-pty", "permit-user-rc"}
	exts := make(map[string]string)
	errSlice := make([]error, 0)

	// Compare each of the config items from our config file against our known-good list, and
	// add them as a key in a map[string]string with empty value, as SSH expects
	for i := range confExts {
		valid := false
		for j := range validExts {
			if confExts[i] == validExts[j] {
				name := confExts[i]
				exts[name] = ""
				valid = true
				break
			}
		}
		if !valid {
			err := fmt.Errorf("Invalid extension in config: %s", confExts[i])
			errSlice = append(errSlice, err)
		}
	}

	return exts, errSlice
}

func getConf() (*config, error) {
	// Read config into a struct
	var conf config
	err := viper.Unmarshal(&conf)
	if err != nil {
		return nil, fmt.Errorf("Unable to read config into struct: %v", err)
	}
	// Hardcoding the DB bucket name
	conf.bucketNameFP = []byte("pubkeybirthdays")
	conf.bucketNameSerial = []byte("certserial")

	// Require TLS mutual authentication for security
	if conf.SSLCA == "" || conf.SSLKey == "" || conf.SSLCert == "" {
		return nil, fmt.Errorf("sslca, sslkey, and sslcert are required fields")
	}

	// Expand $HOME into service user's home path
	conf.DBFile = expandHome(conf.DBFile)

	// Check our certificate extensions (permissions) for validity
	var errSlice []error
	conf.exts, errSlice = validateExtensions(conf.Extensions)
	if len(errSlice) > 0 {
		for _, err := range errSlice {
			log.Printf("%v", err)
		}
	}

	// Compile our user-matching regex (usernames are limited to 32 characters, must start
	// with a-z or _, and contain only these characters: a-z, 0-9, - and _
	// With TLS mutual auth, the certificate fingerprint is used in place of a username ($ssl_client_fingerprint in nginx)
	conf.userRegex = regexp.MustCompile(`(?i)^([a-z_][a-z0-9_-]{0,31}|[a-f0-9]+)$`)

	return &conf, nil
}
