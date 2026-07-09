package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// testKeypair returns a minisign-format public key line and a signer over
// raw messages, built from a fresh ed25519 key.
func testKeypair(t *testing.T, keyID string) (pubLine string, sign func(msg []byte) []byte) {
	t.Helper()
	if len(keyID) != 8 {
		t.Fatalf("keyID must be 8 bytes, got %q", keyID)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append([]byte("Ed"), []byte(keyID)...)
	blob = append(blob, pub...)
	return base64.StdEncoding.EncodeToString(blob), func(msg []byte) []byte {
		return signSigFile(priv, keyID, msg)
	}
}

// signSigFile builds a complete .minisig file for msg: untrusted comment,
// base64(alg || key_id || sig), trusted comment, base64(global sig). The
// global signature covers sig_bytes || trusted_comment_text (minisign
// format spec).
func signSigFile(priv ed25519.PrivateKey, keyID string, msg []byte) []byte {
	sig := ed25519.Sign(priv, msg)
	blob := append([]byte("Ed"), []byte(keyID)...)
	blob = append(blob, sig...)
	trusted := "timestamp:0"
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte(trusted)...))
	out := "untrusted comment: test signature\n" +
		base64.StdEncoding.EncodeToString(blob) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
	return []byte(out)
}

func TestParsePublicKeys(t *testing.T) {
	pubLine, _ := testKeypair(t, "AAAAAAAA")
	keys, err := ParsePublicKeys([]string{pubLine})
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if _, err := ParsePublicKeys([]string{"not base64!!"}); err == nil {
		t.Error("garbage key line accepted")
	}
}

func TestVerifyManifest(t *testing.T) {
	msg := []byte(`{"version":"v0.2.0"}`)
	ciPub, ciSign := testKeypair(t, "AAAAAAAA")
	recPub, _ := testKeypair(t, "BBBBBBBB")
	strangerPub, strangerSign := testKeypair(t, "CCCCCCCC")
	_ = strangerPub

	keyring, err := ParsePublicKeys([]string{ciPub, recPub})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("good signature from a keyring key verifies", func(t *testing.T) {
		if err := VerifyManifest(keyring, msg, ciSign(msg)); err != nil {
			t.Errorf("VerifyManifest: %v", err)
		}
	})
	t.Run("signature from an unknown key is rejected", func(t *testing.T) {
		if err := VerifyManifest(keyring, msg, strangerSign(msg)); err == nil {
			t.Error("unknown key's signature accepted")
		}
	})
	t.Run("tampered message is rejected", func(t *testing.T) {
		sig := ciSign(msg)
		if err := VerifyManifest(keyring, []byte(`{"version":"v9.9.9"}`), sig); err == nil {
			t.Error("tampered message accepted")
		}
	})
	t.Run("garbage signature file is rejected", func(t *testing.T) {
		if err := VerifyManifest(keyring, msg, []byte("garbage")); err == nil {
			t.Error("garbage sig file accepted")
		}
	})
	t.Run("empty keyring is rejected", func(t *testing.T) {
		if err := VerifyManifest(nil, msg, ciSign(msg)); err == nil {
			t.Error("empty keyring accepted a signature")
		}
	})
}

func TestKeyring(t *testing.T) {
	// The key ceremony (Task 17) has embedded the real production keys.
	// Keyring() must parse both without error. The empty-keyring error
	// path is covered by VerifyManifest's "empty keyring is rejected"
	// subtest above.
	keys, err := Keyring()
	if err != nil {
		t.Fatalf("Keyring(): %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("got %d keys, want 2", len(keys))
	}
}
