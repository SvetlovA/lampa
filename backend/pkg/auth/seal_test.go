package auth

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPayload struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

var testNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func testDataKey(fill byte) [32]byte {
	var key [32]byte
	for i := range key {
		key[i] = fill
	}
	return key
}

func newTestCookieSealer(t *testing.T, fill byte) *CookieSealer {
	t.Helper()
	s, err := NewCookieSealer(testDataKey(fill))
	require.NoError(t, err)
	return s
}

func sealTest(t *testing.T, s *CookieSealer, purpose Purpose) string {
	t.Helper()
	value, err := s.Seal(purpose, testPayload{State: "abc", Count: 7}, testNow.Add(10*time.Minute))
	require.NoError(t, err)
	return value
}

func decodeSealed(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(value)
	require.NoError(t, err)
	return raw
}

func TestCookieSealer_RoundTrip(t *testing.T) {
	s := newTestCookieSealer(t, 1)
	for _, purpose := range []Purpose{PurposeLogin, PurposeDevice} {
		t.Run(string(purpose), func(t *testing.T) {
			value := sealTest(t, s, purpose)
			assert.NotContains(t, value, "abc", "payload is not readable in the value")
			assert.NotContains(t, value, "=", "value is unpadded")

			var got testPayload
			require.NoError(t, s.Open(purpose, value, testNow, &got))
			assert.Equal(t, testPayload{State: "abc", Count: 7}, got)
		})
	}
}

func TestCookieSealer_RandomNonce(t *testing.T) {
	s := newTestCookieSealer(t, 1)
	first, second := sealTest(t, s, PurposeLogin), sealTest(t, s, PurposeLogin)
	assert.NotEqual(t, first, second)
}

func TestCookieSealer_OpensWithSameKeyOnly(t *testing.T) {
	value := sealTest(t, newTestCookieSealer(t, 1), PurposeLogin)

	var got testPayload
	require.NoError(t, newTestCookieSealer(t, 1).Open(PurposeLogin, value, testNow, &got), "same data key, new sealer")
	require.ErrorIs(t, newTestCookieSealer(t, 2).Open(PurposeLogin, value, testNow, &got), ErrCookieTampered)
}

func TestCookieSealer_PurposeKeySeparation(t *testing.T) {
	s := newTestCookieSealer(t, 1)
	raw := decodeSealed(t, sealTest(t, s, PurposeLogin))

	// relabel a login value as a device value: the tag now matches, but the device key differs
	copy(raw[:headerSize], purposeHeader(PurposeDevice))
	forged := base64.RawURLEncoding.EncodeToString(raw)

	var got testPayload
	err := s.Open(PurposeDevice, forged, testNow, &got)
	require.ErrorIs(t, err, ErrCookieTampered)
	assert.Empty(t, got)
}

func TestCookieSealer_Expiry(t *testing.T) {
	s := newTestCookieSealer(t, 1)
	value := sealTest(t, s, PurposeLogin)
	expiresAt := testNow.Add(10 * time.Minute)

	tests := []struct {
		name string
		now  time.Time
		err  error
	}{
		{"well before", testNow, nil},
		{"one second before", expiresAt.Add(-time.Second), nil},
		{"at expiry", expiresAt, ErrCookieExpired},
		{"after expiry", expiresAt.Add(time.Hour), ErrCookieExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got testPayload
			err := s.Open(PurposeLogin, value, tt.now, &got)
			if tt.err == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.err)
			require.ErrorIs(t, err, ErrCookieInvalid)
		})
	}
}

func TestCookieSealer_OpenErrors(t *testing.T) {
	s := newTestCookieSealer(t, 1)
	value := sealTest(t, s, PurposeLogin)
	raw := decodeSealed(t, value)
	reencode := func(mutate func(b []byte) []byte) string {
		b := append([]byte(nil), raw...)
		return base64.RawURLEncoding.EncodeToString(mutate(b))
	}

	tests := []struct {
		name    string
		purpose Purpose
		value   string
		err     error
	}{
		{"wrong purpose", PurposeDevice, value, ErrCookieWrongPurpose},
		{"empty", PurposeLogin, "", ErrCookieTruncated},
		{"header only", PurposeLogin, reencode(func(b []byte) []byte { return b[:headerSize] }), ErrCookieTruncated},
		{"missing auth tag", PurposeLogin, reencode(func(b []byte) []byte { return b[:headerSize+12+15] }), ErrCookieTruncated},
		{"dropped last byte", PurposeLogin, reencode(func(b []byte) []byte { return b[:len(b)-1] }), ErrCookieTampered},
		{"flipped ciphertext bit", PurposeLogin, reencode(func(b []byte) []byte { b[len(b)-20] ^= 0x01; return b }), ErrCookieTampered},
		{"flipped nonce bit", PurposeLogin, reencode(func(b []byte) []byte { b[headerSize] ^= 0x01; return b }), ErrCookieTampered},
		{"unknown version", PurposeLogin, reencode(func(b []byte) []byte { b[0] = 0x02; return b }), ErrCookieTampered},
		{"not base64url", PurposeLogin, "!!" + value, ErrCookieTampered},
		{"padded base64", PurposeLogin, value + "=", ErrCookieTampered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got testPayload
			err := s.Open(tt.purpose, tt.value, testNow, &got)
			require.ErrorIs(t, err, tt.err)
			require.ErrorIs(t, err, ErrCookieInvalid)
			assert.Empty(t, got)
			assert.NotContains(t, err.Error(), "abc", "error never contains the payload")
		})
	}
}

func TestCookieSealer_EnvelopeWithoutExpiry(t *testing.T) {
	s := newTestCookieSealer(t, 1)
	// seal a plaintext lacking "exp" directly with the purpose key
	aead := s.aeads[PurposeLogin]
	header := purposeHeader(PurposeLogin)
	nonce := make([]byte, aead.NonceSize())
	sealed := append(append([]byte(nil), header...), nonce...)
	sealed = aead.Seal(sealed, nonce, []byte(`{"data":{"state":"abc"}}`), header)

	var got testPayload
	err := s.Open(PurposeLogin, base64.RawURLEncoding.EncodeToString(sealed), testNow, &got)
	require.ErrorIs(t, err, ErrCookieTampered)
}

func TestCookieSealer_SealErrors(t *testing.T) {
	s := newTestCookieSealer(t, 1)

	_, err := s.Seal(Purpose("lampa-unknown-v1"), testPayload{}, testNow.Add(time.Minute))
	require.ErrorContains(t, err, "unknown purpose")

	_, err = s.Seal(PurposeLogin, testPayload{}, time.Time{})
	require.ErrorContains(t, err, "expiry is required")

	_, err = s.Seal(PurposeLogin, func() {}, testNow.Add(time.Minute))
	require.ErrorContains(t, err, "marshal payload")
}

func TestCookieSealer_OpenErrorsBeforeDecode(t *testing.T) {
	s := newTestCookieSealer(t, 1)

	var got testPayload
	err := s.Open(Purpose("lampa-unknown-v1"), sealTest(t, s, PurposeLogin), testNow, &got)
	require.ErrorContains(t, err, "unknown purpose")

	var wrongShape []string
	err = s.Open(PurposeLogin, sealTest(t, s, PurposeLogin), testNow, &wrongShape)
	require.ErrorContains(t, err, "decode payload")
	require.NotErrorIs(t, err, ErrCookieInvalid, "an authentic value of the wrong shape is a caller bug")
}
