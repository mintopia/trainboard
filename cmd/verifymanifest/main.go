// Command verifymanifest checks a manifest's minisign signature against a
// public key, using the exact same code path the device does at apply time
// (internal/update.ParsePublicKeys + update.VerifyManifest). CI's
// release-dryrun job runs this after the minisign CLI signs and verifies a
// throwaway manifest, to prove go-minisign (what the device actually
// embeds) parses and accepts what the minisign CLI (what CI actually
// signs with) emits — before any real release tag is cut.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mintopia/trainboard/internal/update"
)

type args struct {
	pub, manifest, sig string
}

func main() {
	var a args
	flag.StringVar(&a.pub, "pub", "", "path to a minisign .pub file")
	flag.StringVar(&a.manifest, "m", "", "path to the manifest.json to verify")
	flag.StringVar(&a.sig, "x", "", "path to the manifest's .minisig signature file")
	flag.Parse()
	if err := verify(a); err != nil {
		fmt.Fprintln(os.Stderr, "verifymanifest:", err)
		os.Exit(1)
	}
	fmt.Println("verifymanifest: signature OK")
}

// verify is main's testable body.
func verify(a args) error {
	if a.pub == "" || a.manifest == "" || a.sig == "" {
		return fmt.Errorf("-pub, -m, and -x are all required")
	}
	keyLine, err := lastKeyLine(a.pub)
	if err != nil {
		return fmt.Errorf("reading public key %s: %w", a.pub, err)
	}
	keys, err := update.ParsePublicKeys([]string{keyLine})
	if err != nil {
		return fmt.Errorf("parsing public key %s: %w", a.pub, err)
	}
	manRaw, err := os.ReadFile(a.manifest)
	if err != nil {
		return fmt.Errorf("reading manifest %s: %w", a.manifest, err)
	}
	sigRaw, err := os.ReadFile(a.sig)
	if err != nil {
		return fmt.Errorf("reading signature %s: %w", a.sig, err)
	}
	return update.VerifyManifest(keys, manRaw, sigRaw)
}

// lastKeyLine returns the last non-blank, non-comment line of a minisign
// .pub file — the base64 public key (minisign .pub files are a comment
// line followed by the key line).
func lastKeyLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		last = line
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if last == "" {
		return "", fmt.Errorf("no key line found")
	}
	return last, nil
}
