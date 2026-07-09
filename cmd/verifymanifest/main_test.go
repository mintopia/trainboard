package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The crypto path (does a valid/invalid minisign signature verify?) is
// already covered by internal/update's TestVerifyManifest; the real
// end-to-end coverage for THIS binary is CI's release-dryrun step, which
// runs it against a manifest genuinely signed by the minisign CLI. These
// tests only pin verify's flag/file-error handling.

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyRequiresAllFlags(t *testing.T) {
	pub := writeTempFile(t, "dry.pub", "untrusted comment: x\nRWQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==\n")
	man := writeTempFile(t, "manifest.json", "{}")
	sig := writeTempFile(t, "manifest.json.minisig", "untrusted comment: x\nsignature\n")

	cases := []struct {
		name string
		a    args
	}{
		{"missing pub", args{manifest: man, sig: sig}},
		{"missing manifest", args{pub: pub, sig: sig}},
		{"missing sig", args{pub: pub, manifest: man}},
		{"all missing", args{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := verify(tc.a); err == nil {
				t.Error("verify accepted missing required flag(s)")
			}
		})
	}
}

func TestVerifyMissingPubFile(t *testing.T) {
	man := writeTempFile(t, "manifest.json", "{}")
	sig := writeTempFile(t, "manifest.json.minisig", "sig")
	a := args{pub: filepath.Join(t.TempDir(), "absent.pub"), manifest: man, sig: sig}
	if err := verify(a); err == nil {
		t.Error("verify accepted a nonexistent -pub file")
	}
}

func TestVerifyMissingManifestFile(t *testing.T) {
	pub := writeTempFile(t, "dry.pub", "untrusted comment: x\nRWQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==\n")
	sig := writeTempFile(t, "manifest.json.minisig", "sig")
	a := args{pub: pub, manifest: filepath.Join(t.TempDir(), "absent.json"), sig: sig}
	if err := verify(a); err == nil {
		t.Error("verify accepted a nonexistent -m file")
	}
}

func TestVerifyMissingSigFile(t *testing.T) {
	pub := writeTempFile(t, "dry.pub", "untrusted comment: x\nRWQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==\n")
	man := writeTempFile(t, "manifest.json", "{}")
	a := args{pub: pub, manifest: man, sig: filepath.Join(t.TempDir(), "absent.minisig")}
	if err := verify(a); err == nil {
		t.Error("verify accepted a nonexistent -x file")
	}
}

func TestVerifyRejectsPubFileWithNoKeyLine(t *testing.T) {
	pub := writeTempFile(t, "dry.pub", "# just a comment\n\n")
	man := writeTempFile(t, "manifest.json", "{}")
	sig := writeTempFile(t, "manifest.json.minisig", "sig")
	a := args{pub: pub, manifest: man, sig: sig}
	if err := verify(a); err == nil {
		t.Error("verify accepted a .pub file with no key line")
	}
}

func TestVerifyRejectsMalformedKeyLine(t *testing.T) {
	pub := writeTempFile(t, "dry.pub", "untrusted comment: x\nnot-a-real-key\n")
	man := writeTempFile(t, "manifest.json", "{}")
	sig := writeTempFile(t, "manifest.json.minisig", "sig")
	a := args{pub: pub, manifest: man, sig: sig}
	if err := verify(a); err == nil {
		t.Error("verify accepted a malformed public key line")
	}
}
