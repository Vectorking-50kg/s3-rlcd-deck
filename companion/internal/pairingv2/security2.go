package pairingv2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
)

const (
	security2Scheme       = 2
	security2PatchVersion = 1
	security2Username     = "s3deck-pairing-v2"
	security2PrimeBytes   = 384
	security2SaltBytes    = 16
	security2ProofBytes   = sha512.Size
	security2NonceBytes   = 12
	security2MaximumProto = 1024
)

var (
	errSecurity2State  = errors.New("invalid Security2 session state")
	errSecurity2Proof  = errors.New("Security2 device proof rejected")
	errSecurity2Status = errors.New("Security2 peer rejected request")

	security2Prime = mustSecurity2Prime()
	security2G     = big.NewInt(5)
)

type security2State uint8

const (
	security2New security2State = iota
	security2WaitingResponse0
	security2WaitingResponse1
	security2Ready
	security2Closed
)

// Security2 owns one ESP-IDF protocomm Security2 session. It deliberately does
// not expose its SRP session key. Callers can only advance the authenticated
// state machine and then seal/open endpoint payloads with its ordered nonce.
type Security2 struct {
	mutex    sync.Mutex
	state    security2State
	random   io.Reader
	password []byte
	srp      *srp6aClient
	aead     cipher.AEAD
	nonce    [security2NonceBytes]byte
}

func NewSecurity2(code []byte, random io.Reader) (*Security2, error) {
	if len(code) != 6 {
		return nil, errors.New("Pairing v2 code must contain exactly six digits")
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return nil, errors.New("Pairing v2 code must contain exactly six digits")
		}
	}
	if random == nil {
		random = rand.Reader
	}
	return &Security2{state: security2New, random: random, password: append([]byte(nil), code...)}, nil
}

func (session *Security2) Start() ([]byte, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.state != security2New {
		return nil, errSecurity2State
	}
	srp, err := newSRP6aClient(security2Username, session.password, session.random)
	if err != nil {
		return nil, err
	}
	session.srp = srp
	command := appendProtoBytes(nil, 1, []byte(security2Username))
	command = appendProtoBytes(command, 2, srp.publicKey)
	payload := appendProtoBytes(nil, 20, command)
	document := appendProtoEnum(nil, 2, security2Scheme)
	document = appendProtoBytes(document, 12, payload)
	session.state = security2WaitingResponse0
	return document, nil
}

func (session *Security2) HandleResponse0(document []byte) ([]byte, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.state != security2WaitingResponse0 || session.srp == nil {
		return nil, errSecurity2State
	}
	publicKey, salt, err := parseSecurity2Response0(document)
	if err != nil {
		return nil, err
	}
	proof, err := session.srp.processChallenge(salt, publicKey)
	if err != nil {
		return nil, err
	}
	clearBytes(session.password)
	session.password = nil
	clearBytes(session.srp.password)
	session.srp.password = nil
	command := appendProtoBytes(nil, 1, proof)
	payload := appendProtoEnum(nil, 1, 2)
	payload = appendProtoBytes(payload, 22, command)
	request := appendProtoEnum(nil, 2, security2Scheme)
	request = appendProtoBytes(request, 12, payload)
	session.state = security2WaitingResponse1
	return request, nil
}

func (session *Security2) HandleResponse1(document []byte) error {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.state != security2WaitingResponse1 || session.srp == nil {
		return errSecurity2State
	}
	proof, nonce, err := parseSecurity2Response1(document)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(proof, session.srp.deviceProof) != 1 {
		return errSecurity2Proof
	}
	block, err := aes.NewCipher(session.srp.sessionKey[:32])
	if err != nil {
		return fmt.Errorf("create Security2 cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create Security2 GCM: %w", err)
	}
	copy(session.nonce[:], nonce)
	session.aead = aead
	session.state = security2Ready
	clearBytes(session.password)
	session.password = nil
	return nil
}

func (session *Security2) Seal(plaintext []byte) ([]byte, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.state != security2Ready || session.aead == nil {
		return nil, errSecurity2State
	}
	if binary.BigEndian.Uint32(session.nonce[8:]) == ^uint32(0) {
		return nil, errors.New("Security2 nonce counter exhausted")
	}
	ciphertext := session.aead.Seal(nil, session.nonce[:], plaintext, nil)
	session.incrementNonce()
	return ciphertext, nil
}

func (session *Security2) Open(ciphertext []byte) ([]byte, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.state != security2Ready || session.aead == nil {
		return nil, errSecurity2State
	}
	if binary.BigEndian.Uint32(session.nonce[8:]) == ^uint32(0) {
		return nil, errors.New("Security2 nonce counter exhausted")
	}
	plaintext, err := session.aead.Open(nil, session.nonce[:], ciphertext, nil)
	if err != nil {
		return nil, errors.New("Security2 ciphertext rejected")
	}
	session.incrementNonce()
	return plaintext, nil
}

func (session *Security2) Close() {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	clearBytes(session.password)
	if session.srp != nil {
		session.srp.clear()
	}
	clearBytes(session.nonce[:])
	session.password = nil
	session.srp = nil
	session.aead = nil
	session.state = security2Closed
}

func (session *Security2) incrementNonce() {
	if security2PatchVersion == 1 {
		binary.BigEndian.PutUint32(session.nonce[8:], binary.BigEndian.Uint32(session.nonce[8:])+1)
	}
}

type srp6aClient struct {
	username    []byte
	password    []byte
	private     []byte
	publicKey   []byte
	sessionKey  []byte
	deviceProof []byte
}

func newSRP6aClient(username string, password []byte, random io.Reader) (*srp6aClient, error) {
	private := make([]byte, 32)
	if _, err := io.ReadFull(random, private); err != nil {
		clearBytes(private)
		return nil, fmt.Errorf("generate Security2 client secret: %w", err)
	}
	private[0] |= 0x80
	client := &srp6aClient{
		username: []byte(username),
		password: append([]byte(nil), password...),
		private:  private,
	}
	secret := new(big.Int).SetBytes(private)
	public := new(big.Int).Exp(security2G, secret, security2Prime)
	client.publicKey = padSecurity2Integer(public)
	return client, nil
}

func (client *srp6aClient) processChallenge(salt, publicKey []byte) ([]byte, error) {
	if len(salt) != security2SaltBytes || len(publicKey) != security2PrimeBytes {
		return nil, errors.New("invalid Security2 SRP challenge length")
	}
	serverPublic := new(big.Int).SetBytes(publicKey)
	if new(big.Int).Mod(new(big.Int).Set(serverPublic), security2Prime).Sign() == 0 {
		return nil, errors.New("unsafe Security2 server public key")
	}
	k := hashSecurity2Integer(padSecurity2Integer(security2Prime), padSecurity2Integer(security2G))
	u := hashSecurity2Integer(client.publicKey, publicKey)
	if u.Sign() == 0 {
		return nil, errors.New("unsafe Security2 scrambling parameter")
	}
	identity := make([]byte, 0, len(client.username)+1+len(client.password))
	identity = append(identity, client.username...)
	identity = append(identity, ':')
	identity = append(identity, client.password...)
	identityHash := sha512.Sum512(identity)
	clearBytes(identity)
	xDigest := sha512.New()
	_, _ = xDigest.Write(salt)
	_, _ = xDigest.Write(identityHash[:])
	x := new(big.Int).SetBytes(xDigest.Sum(nil))
	verifier := new(big.Int).Exp(security2G, x, security2Prime)
	base := new(big.Int).Sub(serverPublic, new(big.Int).Mul(k, verifier))
	base.Mod(base, security2Prime)
	exponent := new(big.Int).Mul(u, x)
	exponent.Add(exponent, new(big.Int).SetBytes(client.private))
	shared := new(big.Int).Exp(base, exponent, security2Prime)
	key := sha512.Sum512(shared.Bytes())
	client.sessionKey = append(client.sessionKey[:0], key[:]...)

	hN := sha512.Sum512(padSecurity2Integer(security2Prime))
	hG := sha512.Sum512(padSecurity2Integer(security2G))
	for index := range hN {
		hN[index] ^= hG[index]
	}
	hUsername := sha512.Sum512(client.username)
	proofHash := sha512.New()
	_, _ = proofHash.Write(hN[:])
	_, _ = proofHash.Write(hUsername[:])
	_, _ = proofHash.Write(salt)
	_, _ = proofHash.Write(client.publicKey)
	_, _ = proofHash.Write(publicKey)
	_, _ = proofHash.Write(client.sessionKey)
	proof := proofHash.Sum(nil)
	deviceHash := sha512.New()
	_, _ = deviceHash.Write(client.publicKey)
	_, _ = deviceHash.Write(proof)
	_, _ = deviceHash.Write(client.sessionKey)
	client.deviceProof = deviceHash.Sum(nil)
	return proof, nil
}

func (client *srp6aClient) clear() {
	clearBytes(client.username)
	clearBytes(client.password)
	clearBytes(client.private)
	clearBytes(client.publicKey)
	clearBytes(client.sessionKey)
	clearBytes(client.deviceProof)
}

func mustSecurity2Prime() *big.Int {
	const primeHex = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F83655D23DCA3AD961C62F356208552BB9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3BE39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF6955817183995497CEA956AE515D2261898FA051015728E5A8AAAC42DAD33170D04507A33A85521ABDF1CBA64ECFB850458DBEF0A8AEA71575D060C7DB3970F85A6E1E4C7ABF5AE8CDB0933D71E8C94E04A25619DCEE3D2261AD2EE6BF12FFA06D98A0864D87602733EC86A64521F2B18177B200CBBE117577A615D6C770988C0BAD946E208E24FA074E5AB3143DB5BFCE0FD108E4B82D120A93AD2CAFFFFFFFFFFFFFFFF"
	prime, ok := new(big.Int).SetString(primeHex, 16)
	if !ok || len(prime.Bytes()) != security2PrimeBytes {
		panic("invalid Security2 prime")
	}
	return prime
}

func padSecurity2Integer(value *big.Int) []byte {
	encoded := value.Bytes()
	if len(encoded) > security2PrimeBytes {
		panic("Security2 integer exceeds group width")
	}
	output := make([]byte, security2PrimeBytes)
	copy(output[len(output)-len(encoded):], encoded)
	return output
}

func hashSecurity2Integer(parts ...[]byte) *big.Int {
	hash := sha512.New()
	for _, part := range parts {
		_, _ = hash.Write(part)
	}
	return new(big.Int).SetBytes(hash.Sum(nil))
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func parseSecurity2Response0(document []byte) ([]byte, []byte, error) {
	payload, err := parseSecurity2Session(document, 1, 21)
	if err != nil {
		return nil, nil, err
	}
	fields, err := parseProtoFields(payload, security2MaximumProto)
	if err != nil || rejectUnknownProtoFields(fields, 1, 2, 3) != nil {
		return nil, nil, errors.New("invalid Security2 response0 payload")
	}
	status, err := optionalProtoEnum(fields, 1)
	if err != nil {
		return nil, nil, err
	}
	if status != 0 {
		return nil, nil, fmt.Errorf("%w: status %d", errSecurity2Status, status)
	}
	publicKey, err := requireProtoField(fields, 2, wireBytes)
	if err != nil || len(publicKey.value) != security2PrimeBytes {
		return nil, nil, errors.New("invalid Security2 device public key")
	}
	salt, err := requireProtoField(fields, 3, wireBytes)
	if err != nil || len(salt.value) != security2SaltBytes {
		return nil, nil, errors.New("invalid Security2 device salt")
	}
	return append([]byte(nil), publicKey.value...), append([]byte(nil), salt.value...), nil
}

func parseSecurity2Response1(document []byte) ([]byte, []byte, error) {
	payload, err := parseSecurity2Session(document, 3, 23)
	if err != nil {
		return nil, nil, err
	}
	fields, err := parseProtoFields(payload, security2MaximumProto)
	if err != nil || rejectUnknownProtoFields(fields, 1, 2, 3) != nil {
		return nil, nil, errors.New("invalid Security2 response1 payload")
	}
	status, err := optionalProtoEnum(fields, 1)
	if err != nil {
		return nil, nil, err
	}
	if status != 0 {
		return nil, nil, fmt.Errorf("%w: status %d", errSecurity2Status, status)
	}
	proof, err := requireProtoField(fields, 2, wireBytes)
	if err != nil || len(proof.value) != security2ProofBytes {
		return nil, nil, errors.New("invalid Security2 device proof")
	}
	nonce, err := requireProtoField(fields, 3, wireBytes)
	if err != nil || len(nonce.value) != security2NonceBytes {
		return nil, nil, errors.New("invalid Security2 nonce")
	}
	return append([]byte(nil), proof.value...), append([]byte(nil), nonce.value...), nil
}

func parseSecurity2Session(document []byte, expectedMessage, responseField uint64) ([]byte, error) {
	fields, err := parseProtoFields(document, security2MaximumProto)
	if err != nil || rejectUnknownProtoFields(fields, 2, 12) != nil {
		return nil, errors.New("invalid Security2 session envelope")
	}
	scheme, err := requireProtoField(fields, 2, wireVarint)
	if err != nil || scheme.varint != security2Scheme {
		return nil, errors.New("unsupported Security2 scheme")
	}
	sec2, err := requireProtoField(fields, 12, wireBytes)
	if err != nil {
		return nil, errors.New("missing Security2 payload")
	}
	payloadFields, err := parseProtoFields(sec2.value, security2MaximumProto)
	if err != nil || rejectUnknownProtoFields(payloadFields, 1, responseField) != nil {
		return nil, errors.New("invalid Security2 payload envelope")
	}
	message, err := requireProtoField(payloadFields, 1, wireVarint)
	if err != nil || message.varint != expectedMessage {
		return nil, errors.New("unexpected Security2 response type")
	}
	response, err := requireProtoField(payloadFields, responseField, wireBytes)
	if err != nil {
		return nil, errors.New("missing Security2 response payload")
	}
	return response.value, nil
}

func optionalProtoEnum(fields []protoField, number uint64) (uint64, error) {
	for _, field := range fields {
		if field.number == number {
			if field.wire != wireVarint {
				return 0, fmt.Errorf("protobuf field %d has wrong wire type", number)
			}
			return field.varint, nil
		}
	}
	return 0, nil
}
