// Command devpay is a small devnet tool for trying the shop. -new-keypair creates a keypair file
// in the solana-keygen format and prints its address, to use as the shop's MERCHANT_ADDRESS.
//
// Usage:
//
//	devpay -new-keypair data/merchant.json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	sol "github.com/gagliardetto/solana-go"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the command line and returns the exit code: 0 on success, 1 on failure and 2 on a
// usage error. Results go to stdout (an address) and messages to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devpay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	newKeypair := fs.String("new-keypair", "", "create a keypair `file` (mode 0600), print its address and exit")
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage:
  devpay -new-keypair FILE

devpay creates a devnet keypair file in the solana-keygen format and prints its
address, for example for the shop's MERCHANT_ADDRESS.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *newKeypair == "" || fs.NArg() != 0 {
		return usageError(stderr, "want -new-keypair FILE")
	}
	addr, err := writeKeypair(*newKeypair)
	if err != nil {
		fmt.Fprintf(stderr, "devpay: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, addr)
	fmt.Fprintf(stderr, "Wrote a new keypair to %s, readable only by you. It holds the wallet's secret key:\n"+
		"keep it out of git and use it on devnet only.\n", *newKeypair)
	return 0
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "devpay: %s (devpay -h shows the usage)\n", msg)
	return 2
}

// writeKeypair creates a keypair and writes it to path in the solana-keygen format, a JSON array
// of the 64 secret-key bytes, readable only by the user. It creates the directory if needed and
// never overwrites a file.
func writeKeypair(path string) (sol.PublicKey, error) {
	key, err := sol.NewRandomPrivateKey()
	if err != nil {
		return sol.PublicKey{}, fmt.Errorf("generate keypair: %w", err)
	}
	ints := make([]int, len(key)) // a []byte would marshal as base64
	for i, b := range key {
		ints[i] = int(b)
	}
	data, err := json.Marshal(ints)
	if err != nil {
		return sol.PublicKey{}, fmt.Errorf("encode keypair: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return sol.PublicKey{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return sol.PublicKey{}, err // the error names the file; it says "file exists" for an existing one
	}
	_, err = f.Write(data)
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(path) // a partial key file is useless
		return sol.PublicKey{}, fmt.Errorf("write %s: %w", path, err)
	}
	return key.PublicKey(), nil
}
