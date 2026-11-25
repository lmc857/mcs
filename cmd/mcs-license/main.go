// Command mcs-license signs offline activation codes for MCS.
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"time"

	"mcs/internal/license"
)

func main() {
	var (
		keyFile   = flag.String("key", ".license/mcs-license-private.key", "Ed25519 private key file (base64/hex 64 bytes or PKCS#8 PEM)")
		customer  = flag.String("customer", "", "customer name")
		maxPhones = flag.Int("max-phones", 0, "maximum number of phones")
		machine   = flag.String("machine", "", "machine code shown on the customer's license page")
		licenseID = flag.String("license-id", "", "license id (default: MCS-<timestamp>)")
		issuedAt  = flag.String("issued-at", "", "issue time (default: now, RFC3339)")
		expiresAt = flag.String("expires", "", "expiry date (YYYY-MM-DD or RFC3339); empty = perpetual")
		out       = flag.String("out", "", "optional output file")
	)
	flag.Parse()
	*machine = license.NormalizeMachineCode(*machine)
	if *customer == "" || *maxPhones <= 0 || *machine == "" {
		fmt.Fprintln(os.Stderr, "usage: mcs-license -key keyfile -customer \"客户名\" -max-phones 50 -machine \"MCS-....\"")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read key:", err)
		os.Exit(1)
	}
	priv, err := license.ParsePrivateKey(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse key:", err)
		os.Exit(1)
	}
	if *licenseID == "" {
		*licenseID = fmt.Sprintf("MCS-%d", time.Now().Unix())
	}
	if *issuedAt == "" {
		*issuedAt = time.Now().Format(time.RFC3339)
	}
	if *expiresAt != "" {
		if t, err := time.ParseInLocation("2006-01-02", *expiresAt, time.Local); err == nil {
			*expiresAt = t.Add(24*time.Hour - time.Second).Format(time.RFC3339)
		} else if _, err := time.Parse(time.RFC3339, *expiresAt); err != nil {
			fmt.Fprintln(os.Stderr, "expires must be YYYY-MM-DD or RFC3339")
			os.Exit(2)
		}
	}
	code, err := license.Encode(license.Payload{
		Version: 1, LicenseID: *licenseID, Customer: *customer,
		MaxPhones: *maxPhones, Machine: *machine, IssuedAt: *issuedAt, ExpiresAt: *expiresAt,
	}, ed25519.PrivateKey(priv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
	fmt.Println(code)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(code+"\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}
}
