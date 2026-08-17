package pairingv2

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"testing"
)

// Generated from the Apache-2.0 Espressif Security2/SRP6a reference shipped
// with ESP-IDF v6.0.2. Fixing both client and server ephemerals makes this a
// cross-language contract vector rather than a self-referential Go round trip.
func TestSecurity2MatchesEspressifSRP6aVector(t *testing.T) {
	private := mustDecodeHex(t, "8102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	salt := mustDecodeHex(t, "036ee0c7bcb9eda84c9eac97d93decf4")
	serverPublic := mustDecodeHex(t, "5d2d4cd35414b1979e98f38219bb0496f03dc66cded876008885caec1520e07a30af03e685d14e8f6fc6e61d00bfe3c5bbeb49efeb33be95e6d358da87493caaf3dfed9a4a9c990cea55899e544a24a4460fccc88b863cf6b16c35953a179a69113b1b53893dacac6f1e1172fa552486011b520e9dc4a5fab9dcec79825fb66f8a17839b1a6744469a3a025480156cc155989aca65771be604202763cf74177c0708aab8a00cac5610d5ed5321680afa33a2843ab22bbcba9ff763d9fc1a7eec5a339068b77ae29c40ecd9907237b7df2dde629d68cdd90634c6da998c3455152584cd5dfec8eb5d4f7c788d17ee8bd117bc01bcca8a428d8d28cd54954fc706b7ca135ca00d3f9a6d2c5d8421a5a09b858dc810c7bd1d2fe37c82a751cf9a61f81f85a82f2889da61c698d9b6dd99edc91861a334287d77f0574f0ba9352f7173d30cb8a38e68790e51328b148363b935e26b8070f7371d1edb930a3c4f57f5963cb6f026e66423cfa098fe94eaf027f9d8e40632f801f99868ec0007b6e172")
	expectedPublic := mustDecodeHex(t, "9635466fcdfa383e83d2e0649c05e8fd8b0295462e6e0c1659468d615644c4b84667180402b2ab265d78e2ecc33c9e71c228be66d4a7e3b177738aa947ee262e9691dbd8c66c732b12da139669747e74a2aebcfcfd10c8776fce7a7727d928836451629765e56282025d5047460d6aa621176b1e36ebdd6d6f4734e707bc533028f9f2b7c20e2f6a454c3b8c800e1106c423079fb3b1da6c45d9f9fd309a49cbf2d62332f5e23427e00af763e1fd59b7fc370ff84b8c47784392bd0bb25fc3ff30b2f089e51fa06b8cc1469f1d65c8a1a0b4095d164ff1a2419c5ae8439365b485f7c1fc189ec36de59d8468f8438fd514d41448cccd71d689aa90ec26428947abb3aed4c1011602e8cc8cad8f842b81706c781abed101ab1ae2d47e018eaec2f26a421b56d9af70911551e1d0455bd1b70be2e4a19ea3311ece2e9045cfbe3ccba0b0d05d9bb7830a59489440c624a8a6df431206c55ec2651f62ada8a1c8a17d981884d138eca228640ca912bcd3c788380eef926b03202d7b39cce34b8020")
	expectedProof := mustDecodeHex(t, "df9fe823a30537cd0cc80b1873af82753e0a8ff9cb6e20c0e37e3910bdc2e0fb5ac76dbf38dc9cbc8d725acf1e02960c1e8b6434355ea6b29b95673fdbccfdc3")
	expectedDeviceProof := mustDecodeHex(t, "480118a8c84e253e4de5270586a0838c4b322dc9bc1ec255e7f4cbe5a12b533f793bf3d64b3780d9721efdb8ebea5992275f95ea4f0e817890c18a7b506150b2")
	expectedKey := mustDecodeHex(t, "510949bf0def548581d82da0356b2033afc068819384466218801c866430f0ede0090343f592f96bb5fd15cca137e985b1f8404fc333df55cbe00f212743fd0b")

	session, err := NewSecurity2([]byte("123456"), bytes.NewReader(private))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	request0, err := session.Start()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(session.srp.publicKey, expectedPublic) {
		t.Fatalf("client public key mismatch\n got %x\nwant %x", session.srp.publicKey, expectedPublic)
	}
	assertCommandPublicKey(t, request0, expectedPublic)

	request1, err := session.HandleResponse0(response0Document(serverPublic, salt))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(session.srp.sessionKey, expectedKey) {
		t.Fatalf("session key mismatch\n got %x\nwant %x", session.srp.sessionKey, expectedKey)
	}
	assertCommandProof(t, request1, expectedProof)

	nonce := mustDecodeHex(t, "010203040506070800000009")
	if err := session.HandleResponse1(response1Document(expectedDeviceProof, nonce)); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("Pairing v2 proof endpoint")
	ciphertext, err := session.Seal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(expectedKey[:32])
	aead, _ := cipher.NewGCM(block)
	want := aead.Seal(nil, nonce, plaintext, nil)
	if !bytes.Equal(ciphertext, want) {
		t.Fatalf("ciphertext mismatch\n got %x\nwant %x", ciphertext, want)
	}
	if got := binaryCounter(session.nonce[8:]); got != 10 {
		t.Fatalf("nonce counter = %d, want 10", got)
	}
}

func TestSecurity2RejectsWrongDeviceProofAndCannotBecomeReady(t *testing.T) {
	session, _ := NewSecurity2([]byte("123456"), bytes.NewReader(mustDecodeHex(t, "8102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")))
	defer session.Close()
	_, _ = session.Start()
	serverPublic := mustDecodeHex(t, "5d2d4cd35414b1979e98f38219bb0496f03dc66cded876008885caec1520e07a30af03e685d14e8f6fc6e61d00bfe3c5bbeb49efeb33be95e6d358da87493caaf3dfed9a4a9c990cea55899e544a24a4460fccc88b863cf6b16c35953a179a69113b1b53893dacac6f1e1172fa552486011b520e9dc4a5fab9dcec79825fb66f8a17839b1a6744469a3a025480156cc155989aca65771be604202763cf74177c0708aab8a00cac5610d5ed5321680afa33a2843ab22bbcba9ff763d9fc1a7eec5a339068b77ae29c40ecd9907237b7df2dde629d68cdd90634c6da998c3455152584cd5dfec8eb5d4f7c788d17ee8bd117bc01bcca8a428d8d28cd54954fc706b7ca135ca00d3f9a6d2c5d8421a5a09b858dc810c7bd1d2fe37c82a751cf9a61f81f85a82f2889da61c698d9b6dd99edc91861a334287d77f0574f0ba9352f7173d30cb8a38e68790e51328b148363b935e26b8070f7371d1edb930a3c4f57f5963cb6f026e66423cfa098fe94eaf027f9d8e40632f801f99868ec0007b6e172")
	_, err := session.HandleResponse0(response0Document(serverPublic, mustDecodeHex(t, "036ee0c7bcb9eda84c9eac97d93decf4")))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.HandleResponse1(response1Document(make([]byte, 64), make([]byte, 12))); !errors.Is(err, errSecurity2Proof) {
		t.Fatalf("HandleResponse1 error = %v, want proof rejection", err)
	}
	if _, err := session.Seal([]byte("must not encrypt")); !errors.Is(err, errSecurity2State) {
		t.Fatalf("Seal error = %v, want invalid state", err)
	}
}

func TestSecurity2RejectsMalformedInputs(t *testing.T) {
	for _, code := range [][]byte{nil, []byte("12345"), []byte("12345x"), []byte("1234567")} {
		if _, err := NewSecurity2(code, nil); err == nil {
			t.Fatalf("NewSecurity2(%q) unexpectedly succeeded", code)
		}
	}
	session, _ := NewSecurity2([]byte("123456"), bytes.NewReader(make([]byte, 32)))
	defer session.Close()
	if _, err := session.HandleResponse0([]byte{1}); !errors.Is(err, errSecurity2State) {
		t.Fatalf("response before Start = %v", err)
	}
	if _, err := session.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Start(); !errors.Is(err, errSecurity2State) {
		t.Fatalf("second Start = %v", err)
	}
	if _, err := session.HandleResponse0(response0Document(make([]byte, security2PrimeBytes), make([]byte, security2SaltBytes))); err == nil {
		t.Fatal("zero server public key was accepted")
	}
}

func TestSecurity2RejectsUnknownAndDuplicateProtobufFields(t *testing.T) {
	valid := response0Document(make([]byte, security2PrimeBytes), make([]byte, security2SaltBytes))
	withUnknown := append(append([]byte(nil), valid...), appendProtoEnum(nil, 99, 1)...)
	if _, _, err := parseSecurity2Response0(withUnknown); err == nil {
		t.Fatal("unknown envelope field was accepted")
	}
	duplicateScheme := append(append([]byte(nil), valid...), appendProtoEnum(nil, 2, security2Scheme)...)
	if _, _, err := parseSecurity2Response0(duplicateScheme); err == nil {
		t.Fatal("duplicate envelope field was accepted")
	}
	wrongStatusWire := appendProtoBytes(nil, 1, []byte("success"))
	wrongStatusWire = appendProtoBytes(wrongStatusWire, 2, make([]byte, security2PrimeBytes))
	wrongStatusWire = appendProtoBytes(wrongStatusWire, 3, make([]byte, security2SaltBytes))
	payload := appendProtoEnum(nil, 1, 1)
	payload = appendProtoBytes(payload, 21, wrongStatusWire)
	document := appendProtoEnum(nil, 2, security2Scheme)
	document = appendProtoBytes(document, 12, payload)
	if _, _, err := parseSecurity2Response0(document); err == nil {
		t.Fatal("status with the wrong wire type was accepted")
	}
}

func response0Document(publicKey, salt []byte) []byte {
	response := appendProtoBytes(nil, 2, publicKey)
	response = appendProtoBytes(response, 3, salt)
	payload := appendProtoEnum(nil, 1, 1)
	payload = appendProtoBytes(payload, 21, response)
	document := appendProtoEnum(nil, 2, security2Scheme)
	return appendProtoBytes(document, 12, payload)
}

func response1Document(proof, nonce []byte) []byte {
	response := appendProtoBytes(nil, 2, proof)
	response = appendProtoBytes(response, 3, nonce)
	payload := appendProtoEnum(nil, 1, 3)
	payload = appendProtoBytes(payload, 23, response)
	document := appendProtoEnum(nil, 2, security2Scheme)
	return appendProtoBytes(document, 12, payload)
}

func assertCommandPublicKey(t *testing.T, document, expected []byte) {
	t.Helper()
	fields, _ := parseProtoFields(document, security2MaximumProto)
	sec2, _ := requireProtoField(fields, 12, wireBytes)
	payload, _ := parseProtoFields(sec2.value, security2MaximumProto)
	commandField, _ := requireProtoField(payload, 20, wireBytes)
	command, _ := parseProtoFields(commandField.value, security2MaximumProto)
	username, _ := requireProtoField(command, 1, wireBytes)
	publicKey, _ := requireProtoField(command, 2, wireBytes)
	if string(username.value) != security2Username || !bytes.Equal(publicKey.value, expected) {
		t.Fatal("Security2 command0 did not contain the expected identity and public key")
	}
}

func assertCommandProof(t *testing.T, document, expected []byte) {
	t.Helper()
	fields, _ := parseProtoFields(document, security2MaximumProto)
	sec2, _ := requireProtoField(fields, 12, wireBytes)
	payload, _ := parseProtoFields(sec2.value, security2MaximumProto)
	commandField, _ := requireProtoField(payload, 22, wireBytes)
	command, _ := parseProtoFields(commandField.value, security2MaximumProto)
	proof, _ := requireProtoField(command, 1, wireBytes)
	if !bytes.Equal(proof.value, expected) {
		t.Fatalf("client proof mismatch\n got %x\nwant %x", proof.value, expected)
	}
}

func binaryCounter(value []byte) uint32 {
	return uint32(value[0])<<24 | uint32(value[1])<<16 | uint32(value[2])<<8 | uint32(value[3])
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
